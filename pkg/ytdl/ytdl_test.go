package ytdl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name         string
		format       model.Format
		customFormat feed.CustomFormat
		quality      model.Quality
		maxHeight    int
		output       string
		videoURL     string
		ytdlArgs     []string
		opts         DownloadOptions
		expect       []string
	}{
		{
			name:     "Audio unknown quality",
			format:   model.FormatAudio,
			output:   "/tmp/1",
			videoURL: "http://url",
			expect:   []string{"--extract-audio", "--audio-format", "mp3", "--format", "bestaudio", "--output", "/tmp/1", "http://url"},
		},
		{
			name:     "Audio low quality",
			format:   model.FormatAudio,
			quality:  model.QualityLow,
			output:   "/tmp/1",
			videoURL: "http://url",
			expect:   []string{"--extract-audio", "--audio-format", "mp3", "--format", "worstaudio", "--output", "/tmp/1", "http://url"},
		},
		{
			name:     "Audio best quality",
			format:   model.FormatAudio,
			quality:  model.QualityHigh,
			output:   "/tmp/1",
			videoURL: "http://url",
			expect:   []string{"--extract-audio", "--audio-format", "mp3", "--format", "bestaudio", "--output", "/tmp/1", "http://url"},
		},
		{
			name:     "Video unknown quality",
			format:   model.FormatVideo,
			output:   "/tmp/1",
			videoURL: "http://url",
			expect:   []string{"--format", "bestvideo[ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[ext=mp4][vcodec^=avc1]/best[ext=mp4]/best", "--output", "/tmp/1", "http://url"},
		},
		{
			name:      "Video unknown quality with maxheight",
			format:    model.FormatVideo,
			maxHeight: 720,
			output:    "/tmp/1",
			videoURL:  "http://url",
			expect:    []string{"--format", "bestvideo[ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[ext=mp4][vcodec^=avc1]/best[ext=mp4]/best", "--output", "/tmp/1", "http://url"},
		},
		{
			name:     "Video low quality",
			format:   model.FormatVideo,
			quality:  model.QualityLow,
			output:   "/tmp/2",
			videoURL: "http://url",
			expect:   []string{"--format", "worstvideo[ext=mp4][vcodec^=avc1]+worstaudio[ext=m4a]/worst[ext=mp4][vcodec^=avc1]/worst[ext=mp4]/worst", "--output", "/tmp/2", "http://url"},
		},
		{
			name:      "Video low quality with maxheight",
			format:    model.FormatVideo,
			quality:   model.QualityLow,
			maxHeight: 720,
			output:    "/tmp/2",
			videoURL:  "http://url",
			expect:    []string{"--format", "worstvideo[ext=mp4][vcodec^=avc1]+worstaudio[ext=m4a]/worst[ext=mp4][vcodec^=avc1]/worst[ext=mp4]/worst", "--output", "/tmp/2", "http://url"},
		},
		{
			name:     "Video high quality",
			format:   model.FormatVideo,
			quality:  model.QualityHigh,
			output:   "/tmp/2",
			videoURL: "http://url1",
			expect:   []string{"--format", "bestvideo[ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[ext=mp4][vcodec^=avc1]/best[ext=mp4]/best", "--output", "/tmp/2", "http://url1"},
		},
		{
			name:      "Video high quality with maxheight",
			format:    model.FormatVideo,
			quality:   model.QualityHigh,
			maxHeight: 1024,
			output:    "/tmp/2",
			videoURL:  "http://url1",
			expect:    []string{"--format", "bestvideo[height<=1024][ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[height<=1024][ext=mp4][vcodec^=avc1]/best[ext=mp4]/best", "--output", "/tmp/2", "http://url1"},
		},
		{
			name:     "Video high quality with custom youtube-dl arguments",
			format:   model.FormatVideo,
			quality:  model.QualityHigh,
			output:   "/tmp/2",
			videoURL: "http://url1",
			ytdlArgs: []string{"--write-sub", "--embed-subs", "--sub-lang", "en,en-US,en-GB"},
			expect:   []string{"--format", "bestvideo[ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[ext=mp4][vcodec^=avc1]/best[ext=mp4]/best", "--write-sub", "--embed-subs", "--sub-lang", "en,en-US,en-GB", "--output", "/tmp/2", "http://url1"},
		},
		{
			name:         "Custom format",
			format:       model.FormatCustom,
			customFormat: feed.CustomFormat{YouTubeDLFormat: "bestaudio[ext=m4a]", Extension: "m4a"},
			quality:      model.QualityHigh,
			output:       "/tmp/2",
			videoURL:     "http://url1",
			expect:       []string{"--audio-format", "m4a", "--format", "bestaudio[ext=m4a]", "--output", "/tmp/2", "http://url1"},
		},
		{
			name:     "Audio with subtitles and metadata",
			format:   model.FormatAudio,
			output:   "/tmp/1",
			videoURL: "http://url",
			opts: DownloadOptions{
				WriteInfoJSON: true,
				Subtitles:     true,
				SubLangs:      []string{"en", "de"},
				EmbedMetadata: true,
			},
			expect: []string{
				"--extract-audio", "--audio-format", "mp3", "--format", "bestaudio",
				"--write-info-json",
				"--write-subs", "--write-auto-subs", "--convert-subs", "vtt", "--sub-langs", "en,de",
				"--embed-metadata", "--embed-thumbnail",
				"--output", "/tmp/1", "http://url",
			},
		},
		{
			name:     "Video with embedded chapters",
			format:   model.FormatVideo,
			output:   "/tmp/1",
			videoURL: "http://url",
			opts: DownloadOptions{
				Subtitles:     true,
				EmbedMetadata: true,
				EmbedChapters: true,
			},
			expect: []string{
				"--format", "bestvideo[ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[ext=mp4][vcodec^=avc1]/best[ext=mp4]/best",
				"--write-subs", "--write-auto-subs", "--convert-subs", "vtt",
				"--embed-metadata", "--embed-thumbnail",
				"--embed-chapters",
				"--output", "/tmp/1", "http://url",
			},
		},
		{
			name:     "Custom args come after option flags",
			format:   model.FormatAudio,
			output:   "/tmp/1",
			videoURL: "http://url",
			ytdlArgs: []string{"--no-embed-thumbnail"},
			opts:     DownloadOptions{EmbedMetadata: true},
			expect: []string{
				"--extract-audio", "--audio-format", "mp3", "--format", "bestaudio",
				"--embed-metadata", "--embed-thumbnail",
				"--no-embed-thumbnail",
				"--output", "/tmp/1", "http://url",
			},
		},
	}

	for _, tst := range tests {
		t.Run(tst.name, func(t *testing.T) {
			result := buildArgs(&feed.Config{
				Format:        tst.format,
				Quality:       tst.quality,
				CustomFormat:  tst.customFormat,
				MaxHeight:     tst.maxHeight,
				YouTubeDLArgs: tst.ytdlArgs,
			}, &model.Episode{
				VideoURL: tst.videoURL,
			}, tst.output, tst.opts)

			assert.EqualValues(t, tst.expect, result)
		})
	}
}

func TestDiscoverSidecars(t *testing.T) {
	dir := t.TempDir()

	files := []string{
		"episode1.mp3",
		"episode1.info.json",
		"episode1.en.vtt",
		"episode1.en-US.vtt",
		"episode1.vtt",          // no language token, skipped
		"episode1.en.srt",       // wrong extension, skipped
		"other.en.vtt",          // different base name, skipped
		"episode1.extra.en.vtt", // multi-token middle, skipped
	}
	for _, name := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	infoJSON, subs := discoverSidecars(dir, "episode1")

	assert.Equal(t, filepath.Join(dir, "episode1.info.json"), infoJSON)
	require.Len(t, subs, 2)
	langs := []string{subs[0].Lang, subs[1].Lang}
	assert.ElementsMatch(t, []string{"en", "en-US"}, langs)
}

func TestDiscoverSidecarsEmptyDir(t *testing.T) {
	infoJSON, subs := discoverSidecars(t.TempDir(), "episode1")
	assert.Empty(t, infoJSON)
	assert.Empty(t, subs)
}

func TestDownloadResultClose(t *testing.T) {
	dir, err := os.MkdirTemp("", "podsync-test-")
	require.NoError(t, err)

	mediaPath := filepath.Join(dir, "episode1.mp3")
	require.NoError(t, os.WriteFile(mediaPath, []byte("media"), 0o600))

	result := &DownloadResult{MediaPath: mediaPath, dir: dir}
	assert.Equal(t, dir, result.Dir())

	f, err := result.Open()
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, result.Close())
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err))

	// Close is idempotent
	require.NoError(t, result.Close())
}
