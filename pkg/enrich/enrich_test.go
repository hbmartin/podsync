package enrich

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
)

func TestPickSubtitle(t *testing.T) {
	subtitles := []ytdl.SubtitleFile{
		{Lang: "de", Path: "/x/ep.de.vtt"},
		{Lang: "en-US", Path: "/x/ep.en-US.vtt"},
		{Lang: "en", Path: "/x/ep.en.vtt"},
	}

	assert.Equal(t, "en", pickSubtitle(subtitles, []string{"en"}).Lang)
	assert.Equal(t, "de", pickSubtitle(subtitles, []string{"de", "en"}).Lang)
	assert.Equal(t, "en-US", pickSubtitle(subtitles[:2], []string{"en"}).Lang, "prefix match")
	assert.Equal(t, "de", pickSubtitle(subtitles, []string{"fr"}).Lang, "fallback to first")
	assert.Nil(t, pickSubtitle(nil, []string{"en"}))
}

func TestTranscriptLanguages(t *testing.T) {
	assert.Equal(t, []string{"en"}, TranscriptLanguages(&feed.Config{}))
	assert.Equal(t, []string{"de"}, TranscriptLanguages(&feed.Config{Custom: feed.Custom{Language: "de"}}))
	assert.Equal(t, []string{"fr", "en"}, TranscriptLanguages(&feed.Config{
		Custom:      feed.Custom{Language: "de"},
		Transcripts: &feed.TranscriptsConfig{Languages: []string{"fr", "en"}},
	}))
}

// TestEnrichPlatformArtifacts runs the full enrichment flow with platform
// data only (no external tools, no STT, no ffmpeg image extraction).
func TestEnrichPlatformArtifacts(t *testing.T) {
	workDir := t.TempDir()

	mediaPath := filepath.Join(workDir, "ep1.bin") // extension without embedding support
	require.NoError(t, os.WriteFile(mediaPath, []byte("media"), 0o600))

	subPath := filepath.Join(workDir, "ep1.en.vtt")
	require.NoError(t, os.WriteFile(subPath, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:05.000\nHello world\n"), 0o600))

	infoPath := filepath.Join(workDir, "ep1.info.json")
	require.NoError(t, os.WriteFile(infoPath, []byte(`{"chapters": [
		{"start_time": 0, "end_time": 60, "title": "Intro"},
		{"start_time": 60, "end_time": 120, "title": "Main"}
	]}`), 0o600))

	falseValue := false
	enricher := &Enricher{} // no tools resolved
	result, err := enricher.Enrich(context.Background(), Request{
		FeedConfig: &feed.Config{
			ID:     "feed1",
			Format: model.FormatAudio,
			Chapters: &feed.ChaptersConfig{
				ExtractImages: &falseValue,
			},
		},
		Episode:   &model.Episode{ID: "1", Duration: 120},
		MediaPath: mediaPath,
		InfoJSON:  infoPath,
		Subtitles: []ytdl.SubtitleFile{{Lang: "en", Path: subPath}},
		WorkDir:   workDir,
		BaseName:  "ep1",
		BaseURL:   "http://localhost/feed1",
	})
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(workDir, "ep1.vtt"), result.TranscriptVTT)
	assert.FileExists(t, result.TranscriptVTT)
	assert.Equal(t, "en", result.TranscriptLang)
	assert.Equal(t, SourcePlatform, result.TranscriptSource)

	assert.Equal(t, filepath.Join(workDir, "ep1.transcript.json"), result.TranscriptJSON)
	assert.FileExists(t, result.TranscriptJSON)

	require.Equal(t, filepath.Join(workDir, "ep1.chapters.json"), result.ChaptersJSON)
	assert.Equal(t, SourcePlatform, result.ChaptersSource)

	data, err := os.ReadFile(result.ChaptersJSON)
	require.NoError(t, err)
	var doc ChaptersDoc
	require.NoError(t, json.Unmarshal(data, &doc))
	require.Len(t, doc.Chapters, 2)
	assert.Equal(t, "Intro", doc.Chapters[0].Title)

	enrichment := result.Enrichment()
	require.NotNil(t, enrichment)
	assert.Equal(t, "ep1.vtt", enrichment.TranscriptVTT)
	assert.Equal(t, "ep1.transcript.json", enrichment.TranscriptJSON)
	assert.Equal(t, "ep1.chapters.json", enrichment.ChaptersJSON)
	assert.Empty(t, enrichment.ChapterImages)

	assert.Len(t, result.LocalFiles(), 3)
}

// TestEnrichDescriptionChapters verifies the description fallback and that
// a feed with nothing available yields an empty result without error.
func TestEnrichDescriptionChapters(t *testing.T) {
	workDir := t.TempDir()
	mediaPath := filepath.Join(workDir, "ep1.bin")
	require.NoError(t, os.WriteFile(mediaPath, []byte("media"), 0o600))

	falseValue := false
	enricher := &Enricher{}
	result, err := enricher.Enrich(context.Background(), Request{
		FeedConfig: &feed.Config{
			ID:       "feed1",
			Format:   model.FormatAudio,
			Chapters: &feed.ChaptersConfig{ExtractImages: &falseValue},
		},
		Episode: &model.Episode{
			ID:          "1",
			Duration:    600,
			Description: "00:00 Start\n05:00 End",
		},
		WorkDir:  workDir,
		BaseName: "ep1",
		BaseURL:  "http://localhost/feed1",
	})
	require.NoError(t, err)

	assert.Empty(t, result.TranscriptVTT, "no subtitles and no STT configured")
	require.NotEmpty(t, result.ChaptersJSON)
	assert.Equal(t, SourceDescription, result.ChaptersSource)
}

func TestEnrichDisabled(t *testing.T) {
	falseValue := false
	enricher := &Enricher{}
	result, err := enricher.Enrich(context.Background(), Request{
		FeedConfig: &feed.Config{
			ID:          "feed1",
			Transcripts: &feed.TranscriptsConfig{Enabled: &falseValue},
			Chapters:    &feed.ChaptersConfig{Enabled: &falseValue},
		},
		Episode:  &model.Episode{ID: "1", Description: "00:00 A\n01:00 B"},
		WorkDir:  t.TempDir(),
		BaseName: "ep1",
	})
	require.NoError(t, err)
	assert.Nil(t, result.Enrichment())
	assert.Empty(t, result.LocalFiles())
}

func TestVideoSourceLazy(t *testing.T) {
	calls := 0
	source := &videoSource{req: &Request{
		Episode:   &model.Episode{ID: "1"},
		MediaPath: "/x/ep.mp3",
		FetchVideo: func(context.Context) (string, error) {
			calls++
			return "/x/video.mp4", nil
		},
	}}

	path, err := source.get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/x/video.mp4", path)

	_, _ = source.get(context.Background())
	assert.Equal(t, 1, calls, "video must be fetched at most once")

	// Video feeds use the media file directly
	direct := &videoSource{req: &Request{MediaPath: "/x/ep.mp4"}}
	path, err = direct.get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/x/ep.mp4", path)

	// Disabled fetching
	disabled := &videoSource{req: &Request{MediaPath: "/x/ep.mp3"}}
	_, err = disabled.get(context.Background())
	assert.Error(t, err)
}
