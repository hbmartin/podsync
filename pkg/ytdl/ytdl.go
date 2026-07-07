package ytdl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/model"
)

const (
	DefaultDownloadTimeout = 10 * time.Minute
	UpdatePeriod           = 24 * time.Hour
)

type PlaylistMetadataThumbnail struct {
	Id         string `json:"id"`
	Url        string `json:"url"`
	Resolution string `json:"resolution"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

type PlaylistMetadata struct {
	Id          string                      `json:"id"`
	Title       string                      `json:"title"`
	Description string                      `json:"description"`
	Thumbnails  []PlaylistMetadataThumbnail `json:"thumbnails"`
	Channel     string                      `json:"channel"`
	ChannelId   string                      `json:"channel_id"`
	ChannelUrl  string                      `json:"channel_url"`
	WebpageUrl  string                      `json:"webpage_url"`
}

var (
	ErrTooManyRequests = errors.New(http.StatusText(http.StatusTooManyRequests))
)

// Config is a youtube-dl related configuration
type Config struct {
	// SelfUpdate toggles self update every 24 hour
	SelfUpdate bool `toml:"self_update"`
	// Timeout in minutes for youtube-dl process to finish download
	Timeout int `toml:"timeout"`
	// CustomBinary is a custom path to youtube-dl, this allows using various youtube-dl forks.
	CustomBinary string `toml:"custom_binary"`
}

type YoutubeDl struct {
	path       string
	timeout    time.Duration
	updateLock sync.Mutex // Don't call youtube-dl while self updating
}

func New(ctx context.Context, cfg Config) (*YoutubeDl, error) {
	var (
		path string
		err  error
	)

	if cfg.CustomBinary != "" {
		path = cfg.CustomBinary

		// Don't update custom youtube-dl binaries.
		log.Warnf("using custom youtube-dl binary, turning self updates off")
		cfg.SelfUpdate = false
	} else {
		path, err = exec.LookPath("youtube-dl")
		if err != nil {
			return nil, errors.Wrap(err, "youtube-dl binary not found")
		}

		log.Debugf("found youtube-dl binary at %q", path)
	}

	timeout := DefaultDownloadTimeout
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Minute
	}

	log.Debugf("download timeout: %d min(s)", int(timeout.Minutes()))

	ytdl := &YoutubeDl{
		path:    path,
		timeout: timeout,
	}

	// Make sure youtube-dl exists
	version, err := ytdl.exec(ctx, "--version")
	if err != nil {
		return nil, errors.Wrap(err, "could not find youtube-dl")
	}

	log.Infof("using youtube-dl %s", version)

	if err := ytdl.ensureDependencies(ctx); err != nil {
		return nil, err
	}

	if cfg.SelfUpdate {
		// Do initial blocking update at launch
		if err := ytdl.Update(ctx); err != nil {
			log.WithError(err).Error("failed to update youtube-dl")
		}

		go func() {
			for {
				time.Sleep(UpdatePeriod)

				if err := ytdl.Update(context.Background()); err != nil {
					log.WithError(err).Error("update failed")
				}
			}
		}()
	}

	return ytdl, nil
}

func (dl *YoutubeDl) ensureDependencies(ctx context.Context) error {
	found := false

	if path, err := exec.LookPath("ffmpeg"); err == nil {
		found = true

		output, err := exec.CommandContext(ctx, path, "-version").CombinedOutput()
		if err != nil {
			return errors.Wrap(err, "could not get ffmpeg version")
		}

		log.Infof("found ffmpeg: %s", output)
	}

	if path, err := exec.LookPath("avconv"); err == nil {
		found = true

		output, err := exec.CommandContext(ctx, path, "-version").CombinedOutput()
		if err != nil {
			return errors.Wrap(err, "could not get avconv version")
		}

		log.Infof("found avconv: %s", output)
	}

	if !found {
		return errors.New("either ffmpeg or avconv required to run Podsync")
	}

	return nil
}

func (dl *YoutubeDl) Update(ctx context.Context) error {
	dl.updateLock.Lock()
	defer dl.updateLock.Unlock()

	log.Info("updating youtube-dl")
	output, err := dl.exec(ctx, "--update", "--verbose")
	if err != nil {
		log.WithError(err).Error(output)
		return errors.Wrap(err, "failed to self update youtube-dl")
	}

	log.Info(output)
	return nil
}

func (dl *YoutubeDl) PlaylistMetadata(ctx context.Context, url string) (metadata PlaylistMetadata, err error) {
	log.Info("getting playlist metadata for: ", url)
	args := []string{
		"--playlist-items", "0",
		"-J",            // JSON output
		"-q",            // quiet mode
		"--no-warnings", // suppress warnings
		url,
	}
	dl.updateLock.Lock()
	defer dl.updateLock.Unlock()
	output, err := dl.exec(ctx, args...)
	if err != nil {
		log.WithError(err).Errorf("youtube-dl error: %s", url)

		// YouTube might block host with HTTP Error 429: Too Many Requests
		if strings.Contains(output, "HTTP Error 429") {
			return PlaylistMetadata{}, ErrTooManyRequests
		}

		log.Error(output)
		return PlaylistMetadata{}, errors.New(output)
	}

	var playlistMetadata PlaylistMetadata
	json.Unmarshal([]byte(output), &playlistMetadata)
	return playlistMetadata, nil
}

// DownloadOptions control which sidecar files and embedded metadata are
// requested from yt-dlp in addition to the media itself.
type DownloadOptions struct {
	// WriteInfoJSON requests a .info.json metadata sidecar (contains chapters).
	WriteInfoJSON bool
	// Subtitles requests subtitle sidecars in WebVTT format. Creator-uploaded
	// subtitles are preferred, with auto-generated captions as a fallback.
	Subtitles bool
	// SubLangs is the subtitle language preference list (e.g. ["en", "de"]).
	SubLangs []string
	// EmbedMetadata embeds standard tags and the thumbnail into the media file.
	EmbedMetadata bool
	// EmbedChapters embeds chapter markers into the media container.
	// Only effective for containers that support them (e.g. MP4); it must not
	// be set for MP3 downloads.
	EmbedChapters bool
}

func (dl *YoutubeDl) Download(ctx context.Context, feedConfig *feed.Config, episode *model.Episode, opts DownloadOptions) (r *DownloadResult, err error) {
	tmpDir, err := os.MkdirTemp("", "podsync-")
	if err != nil {
		return nil, errors.Wrap(err, "failed to get temp dir for download")
	}

	defer func() {
		if err != nil {
			err1 := os.RemoveAll(tmpDir)
			if err1 != nil {
				log.Errorf("could not remove temp dir: %v", err1)
			}
		}
	}()

	baseName := feed.EpisodeBaseName(feedConfig, episode)
	// filePath with YoutubeDl template format
	filePath := filepath.Join(tmpDir, fmt.Sprintf("%s.%s", baseName, "%(ext)s"))

	args := buildArgs(feedConfig, episode, filePath, opts)

	dl.updateLock.Lock()
	defer dl.updateLock.Unlock()

	output, err := dl.exec(ctx, args...)
	if err != nil {
		log.WithError(err).Errorf("youtube-dl error: %s", filePath)

		// YouTube might block host with HTTP Error 429: Too Many Requests
		if strings.Contains(output, "HTTP Error 429") {
			return nil, ErrTooManyRequests
		}

		log.Error(output)

		return nil, errors.New(output)
	}

	// filePath now with the final extension
	filePath = filepath.Join(tmpDir, feed.EpisodeName(feedConfig, episode))
	if _, err = os.Stat(filePath); err != nil {
		return nil, errors.Wrap(err, "failed to find downloaded file")
	}

	infoJSON, subtitles := discoverSidecars(tmpDir, baseName)

	return &DownloadResult{
		MediaPath: filePath,
		InfoJSON:  infoJSON,
		Subtitles: subtitles,
		dir:       tmpDir,
	}, nil
}

// FetchVideo downloads a low resolution MP4 of the episode into dir and
// returns its path. It is used to extract chapter frames (and feed AI
// chapter generation) for audio feeds, where the primary download has no
// video track. The caller owns dir and is responsible for cleanup.
func (dl *YoutubeDl) FetchVideo(ctx context.Context, episode *model.Episode, dir string) (string, error) {
	const baseName = "podsync-chapter-video"

	outputTemplate := filepath.Join(dir, baseName+".%(ext)s")
	args := []string{
		"--format", "best[height<=720][ext=mp4]/bestvideo[height<=720][ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best",
		"--output", outputTemplate,
		episode.VideoURL,
	}

	dl.updateLock.Lock()
	defer dl.updateLock.Unlock()

	output, err := dl.exec(ctx, args...)
	if err != nil {
		if strings.Contains(output, "HTTP Error 429") {
			return "", ErrTooManyRequests
		}
		log.Error(output)
		return "", errors.New(output)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", errors.Wrap(err, "failed to scan dir for downloaded video")
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, baseName+".") && !strings.HasSuffix(name, ".part") {
			return filepath.Join(dir, name), nil
		}
	}

	return "", errors.New("downloaded video file not found")
}

func (dl *YoutubeDl) exec(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, dl.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, dl.path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), errors.Wrap(err, "failed to execute youtube-dl")
	}

	return string(output), nil
}

func buildArgs(feedConfig *feed.Config, episode *model.Episode, outputFilePath string, opts DownloadOptions) []string {
	var args []string

	switch feedConfig.Format {
	case model.FormatVideo:
		// Video, mp4, high by default

		format := "bestvideo[ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[ext=mp4][vcodec^=avc1]/best[ext=mp4]/best"

		if feedConfig.Quality == model.QualityLow {
			format = "worstvideo[ext=mp4][vcodec^=avc1]+worstaudio[ext=m4a]/worst[ext=mp4][vcodec^=avc1]/worst[ext=mp4]/worst"
		} else if feedConfig.Quality == model.QualityHigh && feedConfig.MaxHeight > 0 {
			format = fmt.Sprintf("bestvideo[height<=%d][ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[height<=%d][ext=mp4][vcodec^=avc1]/best[ext=mp4]/best", feedConfig.MaxHeight, feedConfig.MaxHeight)
		}

		args = append(args, "--format", format)

	case model.FormatAudio:
		// Audio, mp3, high by default
		format := "bestaudio"
		if feedConfig.Quality == model.QualityLow {
			format = "worstaudio"
		}

		args = append(args, "--extract-audio", "--audio-format", "mp3", "--format", format)

	default:
		args = append(args, "--audio-format", feedConfig.CustomFormat.Extension, "--format", feedConfig.CustomFormat.YouTubeDLFormat)
	}

	if opts.WriteInfoJSON {
		args = append(args, "--write-info-json")
	}

	if opts.Subtitles {
		// Prefer creator-uploaded subtitles; yt-dlp falls back to
		// auto-generated captions for languages without uploaded ones.
		args = append(args, "--write-subs", "--write-auto-subs", "--convert-subs", "vtt")
		if len(opts.SubLangs) > 0 {
			args = append(args, "--sub-langs", strings.Join(opts.SubLangs, ","))
		}
	}

	if opts.EmbedMetadata {
		args = append(args, "--embed-metadata", "--embed-thumbnail")
	}

	if opts.EmbedChapters {
		args = append(args, "--embed-chapters")
	}

	// Insert additional per-feed youtube-dl arguments
	args = append(args, feedConfig.YouTubeDLArgs...)

	args = append(args, "--output", outputFilePath, episode.VideoURL)
	return args
}
