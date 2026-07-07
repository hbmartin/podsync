package enrich

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/enrich/stt"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/media"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
)

// Chapter/transcript source labels recorded in episode metadata.
const (
	SourcePlatform    = "platform"
	SourceDescription = "description"
	SourceLLM         = "llm"
)

// Request describes one downloaded episode to enrich. All paths are local;
// the media file may be modified in place (chapter embedding).
type Request struct {
	FeedConfig *feed.Config
	Episode    *model.Episode
	// MediaPath is the downloaded media file inside WorkDir.
	MediaPath string
	// InfoJSON is the yt-dlp metadata sidecar path, or "".
	InfoJSON string
	// Subtitles are the platform subtitle files downloaded next to the media.
	Subtitles []ytdl.SubtitleFile
	// WorkDir is a scratch directory owned by the caller; enrichment
	// artifacts are written there.
	WorkDir string
	// BaseName is the episode file name without extension.
	BaseName string
	// BaseURL is the public URL prefix of this feed's files
	// (e.g. "https://host/feed1"), used for image links inside chapter JSON.
	BaseURL string
	// FetchVideo lazily downloads a temporary low resolution video for
	// audio feeds. Nil when video fetching is disabled or unavailable.
	FetchVideo func(ctx context.Context) (string, error)
}

// Result lists the enrichment artifacts that were produced. All fields are
// local file paths inside the request's WorkDir; empty means unavailable.
type Result struct {
	TranscriptVTT    string
	TranscriptJSON   string
	TranscriptLang   string
	TranscriptSource string
	ChaptersJSON     string
	ChaptersSource   string
	ChapterImages    []string
}

// Enrichment converts the result into episode metadata, using bare file
// names. Returns nil when nothing was produced.
func (r *Result) Enrichment() *model.EpisodeEnrichment {
	if r.TranscriptVTT == "" && r.TranscriptJSON == "" && r.ChaptersJSON == "" {
		return nil
	}

	enrichment := &model.EpisodeEnrichment{
		TranscriptLang:   r.TranscriptLang,
		TranscriptSource: r.TranscriptSource,
		ChaptersSource:   r.ChaptersSource,
	}
	if r.TranscriptVTT != "" {
		enrichment.TranscriptVTT = filepath.Base(r.TranscriptVTT)
	}
	if r.TranscriptJSON != "" {
		enrichment.TranscriptJSON = filepath.Base(r.TranscriptJSON)
	}
	if r.ChaptersJSON != "" {
		enrichment.ChaptersJSON = filepath.Base(r.ChaptersJSON)
	}
	for _, image := range r.ChapterImages {
		enrichment.ChapterImages = append(enrichment.ChapterImages, filepath.Base(image))
	}
	return enrichment
}

// LocalFiles returns the produced sidecar file paths in a stable order.
func (r *Result) LocalFiles() []string {
	var files []string
	for _, path := range []string{r.TranscriptVTT, r.TranscriptJSON, r.ChaptersJSON} {
		if path != "" {
			files = append(files, path)
		}
	}
	files = append(files, r.ChapterImages...)
	return files
}

// Enricher generates transcripts and chapters for downloaded episodes.
type Enricher struct {
	tools Toolset
}

// New creates an Enricher, resolving the optional helper tools.
func New(toolsCfg feed.ToolsConfig) *Enricher {
	return &Enricher{tools: ResolveTools(toolsCfg)}
}

// Enrich produces transcript and chapter artifacts for one episode.
// It is best-effort: a partial Result is returned together with a
// multierror describing what failed. Callers should publish the episode
// regardless and log the error.
func (e *Enricher) Enrich(ctx context.Context, req Request) (Result, error) {
	var (
		result Result
		errs   *multierror.Error
		video  = &videoSource{req: &req}
	)

	if req.FeedConfig.Transcripts.IsEnabled() {
		if err := e.transcript(ctx, &req, &result); err != nil {
			errs = multierror.Append(errs, errors.Wrap(err, "transcript"))
		}
	}

	if req.FeedConfig.Chapters.IsEnabled() {
		if err := e.chapters(ctx, &req, video, &result); err != nil {
			errs = multierror.Append(errs, errors.Wrap(err, "chapters"))
		}
	}

	return result, errs.ErrorOrNil()
}

// --- transcripts ---

func (e *Enricher) transcript(ctx context.Context, req *Request, result *Result) error {
	vttPath := filepath.Join(req.WorkDir, TranscriptVTTName(req.BaseName))

	lang, source, err := e.obtainVTT(ctx, req, vttPath)
	if err != nil {
		return err
	}
	if source == "" {
		log.WithField("episode_id", req.Episode.ID).Debug("no transcript available")
		return nil
	}

	result.TranscriptVTT = vttPath
	result.TranscriptLang = lang
	result.TranscriptSource = source

	jsonPath := filepath.Join(req.WorkDir, TranscriptJSONName(req.BaseName))
	if err := e.convertTranscript(ctx, vttPath, jsonPath); err != nil {
		log.WithError(err).Warn("failed to convert transcript to PodcastIndex JSON, publishing VTT only")
		return nil
	}
	result.TranscriptJSON = jsonPath

	return nil
}

// obtainVTT places a WebVTT transcript at vttPath, from platform subtitles
// or the STT fallback chain. An empty source with nil error means no
// transcript could be produced and none was configured to be generated.
func (e *Enricher) obtainVTT(ctx context.Context, req *Request, vttPath string) (lang, source string, err error) {
	languages := TranscriptLanguages(req.FeedConfig)

	if subtitle := pickSubtitle(req.Subtitles, languages); subtitle != nil {
		if subtitle.Path != vttPath {
			if err := os.Rename(subtitle.Path, vttPath); err != nil {
				return "", "", errors.Wrap(err, "failed to move subtitle file")
			}
		}
		return subtitle.Lang, SourcePlatform, nil
	}

	sttConfigs := req.FeedConfig.Transcripts.STTProviders()
	if len(sttConfigs) == 0 {
		return "", "", nil
	}

	chain, err := stt.NewChain(sttConfigs, e.tools.FFmpeg)
	if err != nil {
		return "", "", err
	}

	sttLang := ""
	if len(languages) > 0 {
		sttLang = languages[0]
	}

	provider, err := stt.Transcribe(ctx, chain, req.MediaPath, sttLang, vttPath)
	if err != nil {
		return "", "", err
	}
	return sttLang, "stt:" + provider, nil
}

// pickSubtitle returns the best subtitle for the language preference list:
// exact match first, then language-prefix match (en matches en-US), then
// the first available subtitle.
func pickSubtitle(subtitles []ytdl.SubtitleFile, languages []string) *ytdl.SubtitleFile {
	if len(subtitles) == 0 {
		return nil
	}

	for _, lang := range languages {
		for i := range subtitles {
			if strings.EqualFold(subtitles[i].Lang, lang) {
				return &subtitles[i]
			}
		}
		for i := range subtitles {
			if strings.HasPrefix(strings.ToLower(subtitles[i].Lang), strings.ToLower(lang)+"-") {
				return &subtitles[i]
			}
		}
	}
	return &subtitles[0]
}

// TranscriptLanguages returns the subtitle language preference list for a
// feed: the configured transcripts.languages, else the feed's custom.lang,
// else "en".
func TranscriptLanguages(cfg *feed.Config) []string {
	if cfg.Transcripts != nil && len(cfg.Transcripts.Languages) > 0 {
		return cfg.Transcripts.Languages
	}
	if cfg.Custom.Language != "" {
		return []string{cfg.Custom.Language}
	}
	return []string{"en"}
}

// convertTranscript converts VTT to PodcastIndex JSON, preferring the
// transcript2json helper and falling back to the built-in converter.
func (e *Enricher) convertTranscript(ctx context.Context, vttPath, jsonPath string) error {
	if e.tools.Transcript2JSON != "" {
		err := e.convertTranscriptViaTool(ctx, vttPath, jsonPath)
		if err == nil {
			return nil
		}
		log.WithError(err).Debug("transcript2json failed, using built-in converter")
	}
	return ConvertVTTToTranscriptJSON(vttPath, jsonPath)
}

func (e *Enricher) convertTranscriptViaTool(ctx context.Context, vttPath, jsonPath string) error {
	output, err := runTool(ctx, nil, e.tools.Transcript2JSON, vttPath)
	if err != nil {
		return errors.Wrapf(err, "transcript2json failed: %s", truncateOutput(output))
	}

	// The tool writes the converted file next to the input.
	candidates := []string{
		strings.TrimSuffix(vttPath, filepath.Ext(vttPath)) + ".json",
		vttPath + ".json",
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Size() > 0 {
			if candidate == jsonPath {
				return nil
			}
			return os.Rename(candidate, jsonPath)
		}
	}
	return errors.New("transcript2json did not produce an output file")
}

// --- chapters ---

func (e *Enricher) chapters(ctx context.Context, req *Request, video *videoSource, result *Result) error {
	chapters, source := e.findChapters(ctx, req, video)
	if len(chapters) == 0 {
		log.WithField("episode_id", req.Episode.ID).Debug("no chapters available")
		return nil
	}

	fillEndTimes(chapters, float64(req.Episode.Duration))

	var errs *multierror.Error

	if req.FeedConfig.Chapters.ImagesEnabled() {
		if err := e.extractChapterImages(ctx, req, video, chapters, result); err != nil {
			errs = multierror.Append(errs, errors.Wrap(err, "images"))
		}
	}

	jsonPath := filepath.Join(req.WorkDir, ChaptersJSONName(req.BaseName))
	if err := WriteChaptersJSON(chapters, jsonPath); err != nil {
		errs = multierror.Append(errs, err)
		return errs.ErrorOrNil()
	}
	result.ChaptersJSON = jsonPath
	result.ChaptersSource = source

	if err := e.embedChapters(ctx, req, chapters, source); err != nil {
		errs = multierror.Append(errs, errors.Wrap(err, "embedding"))
	}

	return errs.ErrorOrNil()
}

// findChapters tries chapter sources from cheapest to most expensive:
// platform markers, description timestamps, then LLM generation.
func (e *Enricher) findChapters(ctx context.Context, req *Request, video *videoSource) ([]Chapter, string) {
	if req.InfoJSON != "" {
		chapters, err := infoJSONChapters(req.InfoJSON)
		if err != nil {
			log.WithError(err).Warn("failed to parse chapters from info.json")
		} else if len(chapters) > 0 {
			return chapters, SourcePlatform
		}
	}

	if chapters := e.descriptionChapters(ctx, req); len(chapters) > 0 {
		return chapters, SourceDescription
	}

	if req.FeedConfig.Chapters.LLMConfigured() {
		chapters, err := e.llmChapters(ctx, req, video)
		if err != nil {
			log.WithError(err).Warn("LLM chapter generation failed")
		} else if len(chapters) > 0 {
			return chapters, SourceLLM
		}
	}

	return nil, ""
}

// descriptionChapters parses chapter timestamps out of the episode
// description, preferring the podcast-chapters helper tool.
func (e *Enricher) descriptionChapters(ctx context.Context, req *Request) []Chapter {
	description := req.Episode.Description
	if strings.TrimSpace(description) == "" {
		return nil
	}

	if e.tools.PodcastChapters != "" {
		chapters, err := e.descriptionChaptersViaTool(ctx, req.WorkDir, description)
		if err != nil {
			log.WithError(err).Debug("podcast-chapters failed, using built-in description parser")
		} else if len(chapters) > 0 {
			return chapters
		}
	}

	return ParseDescriptionChapters(description)
}

func (e *Enricher) descriptionChaptersViaTool(ctx context.Context, workDir, description string) ([]Chapter, error) {
	inputPath := filepath.Join(workDir, "podsync-description.txt")
	if err := os.WriteFile(inputPath, []byte(description), 0o600); err != nil {
		return nil, errors.Wrap(err, "failed to write description file")
	}
	defer os.Remove(inputPath)

	outputPath := filepath.Join(workDir, "podsync-description-chapters.json")
	defer os.Remove(outputPath)

	output, err := runTool(ctx, nil, e.tools.PodcastChapters,
		"from-description", inputPath, "--to", "pci", "--output", outputPath)
	if err != nil {
		return nil, errors.Wrapf(err, "podcast-chapters failed: %s", truncateOutput(output))
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, errors.Wrap(err, "podcast-chapters did not produce an output file")
	}
	return parseFlexibleChapters(data)
}

// llmChapters generates chapters from the video content using the
// video-to-chapters-with-transcript helper (AssemblyAI + Gemini).
func (e *Enricher) llmChapters(ctx context.Context, req *Request, video *videoSource) ([]Chapter, error) {
	if e.tools.VideoToChapters == "" {
		return nil, errors.New("video-to-chapters-with-transcript tool not found")
	}

	videoPath, err := video.get(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "no video available for chapter generation")
	}

	outputPath := filepath.Join(req.WorkDir, "podsync-llm-chapters.json")
	defer os.Remove(outputPath)

	llm := req.FeedConfig.Chapters.LLMSettings()
	env := []string{
		"ASSEMBLYAI_API_KEY=" + llm.AssemblyAIKey,
		"GEMINI_API_KEY=" + llm.GeminiKey,
	}

	output, err := runTool(ctx, env, e.tools.VideoToChapters,
		videoPath, "--export", "json", "--output", outputPath)
	if err != nil {
		return nil, errors.Wrapf(err, "video-to-chapters failed: %s", truncateOutput(output))
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		// Some versions print JSON to stdout instead of writing a file.
		if chapters, parseErr := parseFlexibleChapters([]byte(output)); parseErr == nil {
			return chapters, nil
		}
		return nil, errors.Wrap(err, "video-to-chapters did not produce an output file")
	}
	return parseFlexibleChapters(data)
}

func (e *Enricher) extractChapterImages(ctx context.Context, req *Request, video *videoSource, chapters []Chapter, result *Result) error {
	if e.tools.FFmpeg == "" {
		return errors.New("ffmpeg not available")
	}

	videoPath, err := video.get(ctx)
	if err != nil {
		return err
	}

	duration := float64(req.Episode.Duration)
	for i := range chapters {
		// Frame at chapter start plus one second, to skip transitions.
		at := chapters[i].StartTime + 1
		if duration > 0 && at >= duration {
			at = chapters[i].StartTime
		}

		name := ChapterImageName(req.BaseName, i, len(chapters))
		outPath := filepath.Join(req.WorkDir, name)

		if err := media.ExtractFrame(ctx, e.tools.FFmpeg, videoPath, at, req.FeedConfig.Chapters.ImageWidth(), outPath); err != nil {
			log.WithError(err).Warnf("failed to extract frame for chapter %d", i+1)
			continue
		}

		chapters[i].Img = req.BaseURL + "/" + name
		result.ChapterImages = append(result.ChapterImages, outPath)
	}
	return nil
}

// embedChapters writes chapter markers into the media file itself.
func (e *Enricher) embedChapters(ctx context.Context, req *Request, chapters []Chapter, source string) error {
	switch strings.ToLower(filepath.Ext(req.MediaPath)) {
	case ".mp3":
		return media.EmbedID3Chapters(req.MediaPath, toMediaChapters(chapters))
	case ".mp4", ".m4a", ".m4v", ".mov":
		// Platform chapters are already embedded by yt-dlp (--embed-chapters)
		// for video feeds; only generated chapters need a remux.
		if source == SourcePlatform && req.FeedConfig.Format == model.FormatVideo {
			return nil
		}
		if e.tools.FFmpeg == "" {
			return errors.New("ffmpeg not available for chapter embedding")
		}
		return media.EmbedMP4Chapters(ctx, e.tools.FFmpeg, req.MediaPath, toMediaChapters(chapters))
	default:
		// Unsupported container; feed-level chapters still work.
		return nil
	}
}

// --- helpers ---

// videoSource lazily resolves a local video file for frame extraction and
// LLM chapter generation, fetching it at most once.
type videoSource struct {
	req  *Request
	path string
	err  error
	done bool
}

func (v *videoSource) get(ctx context.Context) (string, error) {
	if v.done {
		return v.path, v.err
	}
	v.done = true

	if isVideoFile(v.req.MediaPath) {
		v.path = v.req.MediaPath
		return v.path, nil
	}

	if v.req.FetchVideo == nil {
		v.err = errors.New("video fetching is disabled")
		return "", v.err
	}

	log.WithField("episode_id", v.req.Episode.ID).Info("fetching temporary video for chapter processing")
	v.path, v.err = v.req.FetchVideo(ctx)
	return v.path, v.err
}

func isVideoFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov", ".mkv", ".webm", ".avi":
		return true
	default:
		return false
	}
}

func fillEndTimes(chapters []Chapter, totalDuration float64) {
	for i := range chapters {
		if chapters[i].EndTime != 0 {
			continue
		}
		if i+1 < len(chapters) {
			chapters[i].EndTime = chapters[i+1].StartTime
		} else if totalDuration > chapters[i].StartTime {
			chapters[i].EndTime = totalDuration
		} else {
			chapters[i].EndTime = chapters[i].StartTime
		}
	}
}

func toMediaChapters(chapters []Chapter) []media.Chapter {
	converted := make([]media.Chapter, 0, len(chapters))
	for _, chapter := range chapters {
		converted = append(converted, media.Chapter{
			Start: secondsToDuration(chapter.StartTime),
			End:   secondsToDuration(chapter.EndTime),
			Title: chapter.Title,
		})
	}
	return converted
}

func secondsToDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func truncateOutput(output string) string {
	const limit = 512
	output = strings.TrimSpace(output)
	if len(output) > limit {
		return output[:limit] + "..."
	}
	return output
}
