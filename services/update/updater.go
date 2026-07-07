package update

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/builder"
	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/enrich"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
)

type Downloader interface {
	Download(ctx context.Context, feedConfig *feed.Config, episode *model.Episode, opts ytdl.DownloadOptions) (*ytdl.DownloadResult, error)
	FetchVideo(ctx context.Context, episode *model.Episode, dir string) (string, error)
	PlaylistMetadata(ctx context.Context, url string) (metadata ytdl.PlaylistMetadata, err error)
}

// Enricher generates transcript/chapter sidecars for downloaded episodes.
type Enricher interface {
	Enrich(ctx context.Context, req enrich.Request) (enrich.Result, error)
}

type TokenList []string

type Manager struct {
	hostname   string
	downloader Downloader
	enricher   Enricher
	db         db.Storage
	fs         fs.Storage
	feeds      map[string]*feed.Config
	keys       map[model.Provider]feed.KeyProvider
}

func NewUpdater(
	feeds map[string]*feed.Config,
	keys map[model.Provider]feed.KeyProvider,
	hostname string,
	downloader Downloader,
	enricher Enricher,
	db db.Storage,
	fs fs.Storage,
) (*Manager, error) {
	return &Manager{
		hostname:   hostname,
		downloader: downloader,
		enricher:   enricher,
		db:         db,
		fs:         fs,
		feeds:      feeds,
		keys:       keys,
	}, nil
}

func (u *Manager) Update(ctx context.Context, feedConfig *feed.Config) error {
	log.WithFields(log.Fields{
		"feed_id": feedConfig.ID,
		"format":  feedConfig.Format,
		"quality": feedConfig.Quality,
	}).Infof("-> updating %s", feedConfig.URL)

	started := time.Now()

	if err := u.updateFeed(ctx, feedConfig); err != nil {
		return errors.Wrap(err, "update failed")
	}

	// Fetch episodes for download
	episodesToDownload, err := u.fetchEpisodes(ctx, feedConfig)
	if err != nil {
		return errors.Wrap(err, "fetch episodes failed")
	}

	if err := u.downloadEpisodes(ctx, feedConfig, episodesToDownload); err != nil {
		return errors.Wrap(err, "download failed")
	}

	if err := u.cleanup(ctx, feedConfig); err != nil {
		log.WithError(err).Error("cleanup failed")
	}

	if err := u.buildXML(ctx, feedConfig); err != nil {
		return errors.Wrap(err, "xml build failed")
	}

	if err := u.buildOPML(ctx); err != nil {
		return errors.Wrap(err, "opml build failed")
	}

	elapsed := time.Since(started)
	log.Infof("successfully updated feed in %s", elapsed)
	return nil
}

// updateFeed pulls API for new episodes and saves them to database
func (u *Manager) updateFeed(ctx context.Context, feedConfig *feed.Config) error {
	info, err := builder.ParseURL(feedConfig.URL)
	if err != nil {
		return errors.Wrapf(err, "failed to parse URL: %s", feedConfig.URL)
	}

	keyProvider, ok := u.keys[info.Provider]
	if !ok {
		return errors.Errorf("key provider %q not loaded", info.Provider)
	}

	// Create an updater for this feed type
	provider, err := builder.New(ctx, info.Provider, keyProvider.Get(), u.downloader)
	if err != nil {
		return err
	}

	// Query API to get episodes
	log.Debug("building feed")
	result, err := provider.Build(ctx, feedConfig)
	if err != nil {
		return err
	}

	log.Debugf("received %d episode(s) for %q", len(result.Episodes), result.Title)

	episodeSet := make(map[string]struct{})
	if err := u.db.WalkEpisodes(ctx, feedConfig.ID, func(episode *model.Episode) error {
		if episode.Status != model.EpisodeDownloaded && episode.Status != model.EpisodeCleaned {
			episodeSet[episode.ID] = struct{}{}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := u.db.AddFeed(ctx, feedConfig.ID, result); err != nil {
		return err
	}

	for _, episode := range result.Episodes {
		delete(episodeSet, episode.ID)
	}

	// removing episodes that are no longer available in the feed and not downloaded or cleaned
	for id := range episodeSet {
		log.Infof("removing episode %q", id)
		err := u.db.DeleteEpisode(feedConfig.ID, id)
		if err != nil {
			return err
		}
	}

	log.Debug("successfully saved updates to storage")
	return nil
}

func (u *Manager) fetchEpisodes(ctx context.Context, feedConfig *feed.Config) ([]*model.Episode, error) {
	var (
		feedID       = feedConfig.ID
		downloadList []*model.Episode
		pageSize     = feedConfig.PageSize
	)

	log.WithField("page_size", pageSize).Info("fetching episodes for download")

	// Build the list of files to download
	err := u.db.WalkEpisodes(ctx, feedID, func(episode *model.Episode) error {
		var (
			logger = log.WithFields(log.Fields{"episode_id": episode.ID})
		)
		if episode.Status != model.EpisodeNew && episode.Status != model.EpisodeError {
			// File already downloaded
			logger.Infof("skipping due to already downloaded")
			return nil
		}

		if !matchFilters(episode, &feedConfig.Filters) {
			return nil
		}

		// Limit the number of episodes downloaded at once
		pageSize--
		if pageSize < 0 {
			return nil
		}

		log.Debugf("adding %s (%q) to queue", episode.ID, episode.Title)
		downloadList = append(downloadList, episode)
		return nil
	})

	if err != nil {
		return nil, errors.Wrapf(err, "failed to build update list")
	}

	return downloadList, nil
}

func (u *Manager) downloadEpisodes(ctx context.Context, feedConfig *feed.Config, downloadList []*model.Episode) error {
	var (
		downloadCount = len(downloadList)
		downloaded    = 0
		feedID        = feedConfig.ID
	)

	if downloadCount > 0 {
		log.Infof("download count: %d", downloadCount)
	} else {
		log.Info("no episodes to download")
		return nil
	}

	// Download pending episodes

	for idx, episode := range downloadList {
		var (
			logger      = log.WithFields(log.Fields{"index": idx, "episode_id": episode.ID})
			episodeName = feed.EpisodeName(feedConfig, episode)
		)

		// Check whether episode already exists
		size, err := u.fs.Size(ctx, fmt.Sprintf("%s/%s", feedID, episodeName))
		if err == nil {
			logger.Infof("episode %q already exists on disk", episode.ID)

			// File already exists, update file status and disk size
			if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
				episode.Size = size
				episode.Status = model.EpisodeDownloaded
				return nil
			}); err != nil {
				logger.WithError(err).Error("failed to update file info")
				return err
			}

			continue
		} else if os.IsNotExist(err) {
			// Will download, do nothing here
		} else {
			logger.WithError(err).Error("failed to stat file")
			return err
		}

		// Download episode to disk
		// We download the episode to a temp directory first to avoid downloading this file by clients
		// while still being processed by youtube-dl (e.g. a file is being downloaded from YT or encoding in progress)

		logger.Infof("! downloading episode %s", episode.VideoURL)
		result, err := u.downloader.Download(ctx, feedConfig, episode, downloadOptions(feedConfig))
		if err != nil {
			// YouTube might block host with HTTP Error 429: Too Many Requests
			// We still need to generate XML, so just stop sending download requests and
			// retry next time
			if err == ytdl.ErrTooManyRequests {
				logger.Warn("server responded with a 'Too Many Requests' error")
				break
			}

			// Execute episode download error hooks
			if len(feedConfig.OnEpisodeDownloadError) > 0 {
				env := []string{
					"FEED_NAME=" + feedID,
					"EPISODE_TITLE=" + episode.Title,
					"ERROR_MESSAGE=" + err.Error(),
				}

				for i, hook := range feedConfig.OnEpisodeDownloadError {
					if hookErr := hook.Invoke(env); hookErr != nil {
						logger.Errorf("failed to execute episode download error hook %d: %v", i+1, hookErr)
					} else {
						logger.Infof("episode download error hook %d executed successfully", i+1)
					}
				}
			}

			if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
				episode.Status = model.EpisodeError
				return nil
			}); err != nil {
				return err
			}

			continue
		}

		// Enrich the episode with transcripts and chapters before copying,
		// since chapter embedding modifies the media file in place. This is
		// best effort: failures are logged and the episode is published
		// without the missing pieces (enrichment is attempted exactly once).
		var enrichment *model.EpisodeEnrichment
		var sidecars []string
		if u.enricher != nil {
			enrichResult, enrichErr := u.enricher.Enrich(ctx, u.enrichRequest(feedConfig, episode, result))
			if enrichErr != nil {
				logger.WithError(enrichErr).Warn("episode enrichment incomplete")
			}
			enrichment = enrichResult.Enrichment()
			sidecars = enrichResult.LocalFiles()
		}

		logger.Debug("copying file")
		mediaFile, err := result.Open()
		if err != nil {
			result.Close()
			logger.WithError(err).Error("failed to open downloaded file")
			return err
		}

		fileSize, err := u.fs.Create(ctx, fmt.Sprintf("%s/%s", feedID, episodeName), mediaFile)
		mediaFile.Close()
		if err != nil {
			result.Close()
			logger.WithError(err).Error("failed to copy file")
			return err
		}

		enrichment = u.copySidecars(ctx, feedID, sidecars, enrichment, logger)
		result.Close()

		// Execute post episode download hooks
		if len(feedConfig.PostEpisodeDownload) > 0 {
			env := []string{
				"EPISODE_FILE=" + fmt.Sprintf("%s/%s", feedID, episodeName),
				"FEED_NAME=" + feedID,
				"EPISODE_TITLE=" + episode.Title,
			}

			for i, hook := range feedConfig.PostEpisodeDownload {
				if err := hook.Invoke(env); err != nil {
					logger.Errorf("failed to execute post episode download hook %d: %v", i+1, err)
				} else {
					logger.Infof("post episode download hook %d executed successfully", i+1)
				}
			}
		}

		// Update file status in database

		logger.Infof("successfully downloaded file %q", episode.ID)
		if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
			episode.Size = fileSize
			episode.Status = model.EpisodeDownloaded
			episode.Enrichment = enrichment
			return nil
		}); err != nil {
			return err
		}

		downloaded++
	}

	log.Infof("downloaded %d episode(s)", downloaded)
	return nil
}

// downloadOptions derives yt-dlp sidecar/embedding options from the feed's
// transcript and chapter configuration.
func downloadOptions(feedConfig *feed.Config) ytdl.DownloadOptions {
	opts := ytdl.DownloadOptions{
		// Standard tags and cover art make downloaded files look right in
		// any player, not only podcast apps.
		EmbedMetadata: feedConfig.Format == model.FormatAudio || feedConfig.Format == model.FormatVideo,
	}

	if feedConfig.Transcripts.IsEnabled() {
		opts.Subtitles = true
		opts.SubLangs = enrich.TranscriptLanguages(feedConfig)
	}

	if feedConfig.Chapters.IsEnabled() {
		opts.WriteInfoJSON = true
		// MP4 containers support chapter markers natively; MP3 chapters are
		// embedded separately as ID3 frames after download.
		opts.EmbedChapters = feedConfig.Format == model.FormatVideo
	}

	return opts
}

// enrichRequest assembles the enrichment input for a downloaded episode.
func (u *Manager) enrichRequest(feedConfig *feed.Config, episode *model.Episode, result *ytdl.DownloadResult) enrich.Request {
	req := enrich.Request{
		FeedConfig: feedConfig,
		Episode:    episode,
		MediaPath:  result.MediaPath,
		InfoJSON:   result.InfoJSON,
		Subtitles:  result.Subtitles,
		WorkDir:    result.Dir(),
		BaseName:   feed.EpisodeBaseName(feedConfig, episode),
		BaseURL:    fmt.Sprintf("%s/%s", strings.TrimRight(u.hostname, "/"), feedConfig.ID),
	}

	// Audio feeds have no video track; allow the enricher to fetch a
	// temporary low resolution video for chapter frames and AI chapter
	// generation, unless disabled.
	if feedConfig.Format != model.FormatVideo && feedConfig.Chapters.VideoFetchEnabled() {
		dir := result.Dir()
		req.FetchVideo = func(ctx context.Context) (string, error) {
			return u.downloader.FetchVideo(ctx, episode, dir)
		}
	}

	return req
}

// copySidecars uploads enrichment sidecar files to storage and prunes
// episode metadata entries for files that failed to copy, so the feed never
// references missing files. Chapter images are kept for cleanup tracking
// even if the chapters JSON was dropped.
func (u *Manager) copySidecars(ctx context.Context, feedID string, sidecars []string, enrichment *model.EpisodeEnrichment, logger *log.Entry) *model.EpisodeEnrichment {
	if enrichment == nil {
		return nil
	}

	copied := make(map[string]bool, len(sidecars))
	for _, localPath := range sidecars {
		name := filepath.Base(localPath)

		f, err := os.Open(localPath)
		if err != nil {
			logger.WithError(err).Errorf("failed to open sidecar file %q", name)
			continue
		}

		_, err = u.fs.Create(ctx, fmt.Sprintf("%s/%s", feedID, name), f)
		f.Close()
		if err != nil {
			logger.WithError(err).Errorf("failed to copy sidecar file %q", name)
			continue
		}

		copied[name] = true
	}

	if !copied[enrichment.TranscriptVTT] {
		enrichment.TranscriptVTT = ""
	}
	if !copied[enrichment.TranscriptJSON] {
		enrichment.TranscriptJSON = ""
	}
	if enrichment.TranscriptVTT == "" && enrichment.TranscriptJSON == "" {
		enrichment.TranscriptLang = ""
		enrichment.TranscriptSource = ""
	}
	if !copied[enrichment.ChaptersJSON] {
		enrichment.ChaptersJSON = ""
		enrichment.ChaptersSource = ""
	}

	images := enrichment.ChapterImages[:0]
	for _, image := range enrichment.ChapterImages {
		if copied[image] {
			images = append(images, image)
		}
	}
	enrichment.ChapterImages = images

	if len(enrichment.SidecarFiles()) == 0 {
		return nil
	}
	return enrichment
}

func (u *Manager) buildXML(ctx context.Context, feedConfig *feed.Config) error {
	f, err := u.db.GetFeed(ctx, feedConfig.ID)
	if err != nil {
		return err
	}

	// Build iTunes XML feed with data received from builder
	log.Debug("building iTunes podcast feed")
	podcast, err := feed.Build(ctx, f, feedConfig, u.hostname)
	if err != nil {
		return err
	}

	var (
		reader  = bytes.NewReader([]byte(podcast.String()))
		xmlName = fmt.Sprintf("%s.xml", feedConfig.ID)
	)

	if _, err := u.fs.Create(ctx, xmlName, reader); err != nil {
		return errors.Wrap(err, "failed to upload new XML feed")
	}

	return nil
}

func (u *Manager) buildOPML(ctx context.Context) error {
	// Build OPML with data received from builder
	log.Debug("building podcast OPML")
	opml, err := feed.BuildOPML(ctx, u.feeds, u.db, u.hostname)
	if err != nil {
		return err
	}

	var (
		reader  = bytes.NewReader([]byte(opml))
		xmlName = fmt.Sprintf("%s.opml", "podsync")
	)

	if _, err := u.fs.Create(ctx, xmlName, reader); err != nil {
		return errors.Wrap(err, "failed to upload OPML")
	}

	return nil
}

func (u *Manager) cleanup(ctx context.Context, feedConfig *feed.Config) error {
	var (
		feedID = feedConfig.ID
		logger = log.WithField("feed_id", feedID)
		list   []*model.Episode
		result *multierror.Error
	)

	if feedConfig.Clean == nil {
		logger.Debug("no cleanup policy configured")
		return nil
	}

	count := feedConfig.Clean.KeepLast
	if count < 1 {
		logger.Info("nothing to clean")
		return nil
	}

	logger.WithField("count", count).Info("running cleaner")
	if err := u.db.WalkEpisodes(ctx, feedConfig.ID, func(episode *model.Episode) error {
		if episode.Status == model.EpisodeDownloaded {
			list = append(list, episode)
		}
		return nil
	}); err != nil {
		return err
	}

	if count > len(list) {
		return nil
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].PubDate.After(list[j].PubDate)
	})

	for _, episode := range list[count:] {
		logger.WithField("episode_id", episode.ID).Infof("deleting %q", episode.Title)

		var (
			episodeName = feed.EpisodeName(feedConfig, episode)
			path        = fmt.Sprintf("%s/%s", feedConfig.ID, episodeName)
		)

		err := u.fs.Delete(ctx, path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.WithError(err).Errorf("failed to delete episode file: %s", episode.ID)
				result = multierror.Append(result, errors.Wrapf(err, "failed to delete episode: %s", episode.ID))
				continue
			}

			logger.WithField("episode_id", episode.ID).Info("episode was not found - file does not exist")
		}

		// Delete transcript/chapter sidecar files along with the media
		for _, sidecar := range episode.Enrichment.SidecarFiles() {
			sidecarPath := fmt.Sprintf("%s/%s", feedConfig.ID, sidecar)
			if err := u.fs.Delete(ctx, sidecarPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.WithError(err).Errorf("failed to delete sidecar file: %s", sidecarPath)
				result = multierror.Append(result, errors.Wrapf(err, "failed to delete sidecar: %s", sidecarPath))
			}
		}

		if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
			episode.Status = model.EpisodeCleaned
			episode.Title = ""
			episode.Description = ""
			episode.Enrichment = nil
			return nil
		}); err != nil {
			result = multierror.Append(result, errors.Wrapf(err, "failed to set state for cleaned episode: %s", episode.ID))
			continue
		}
	}

	return result.ErrorOrNil()
}
