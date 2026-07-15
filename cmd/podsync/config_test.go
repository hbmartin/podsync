package main

import (
	"os"
	"testing"
	"time"

	"github.com/mxpv/podsync/services/web"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/model"
)

func TestLoadConfig(t *testing.T) {
	const file = `
[tokens]
youtube = "123"
vimeo = ["321", "456"]

[server]
port = 80
data_dir = "test/data/"

[database]
dir = "/home/user/db/"

[downloader]
self_update = true
timeout = 15

[feeds]
  [feeds.XYZ]
  url = "https://youtube.com/watch?v=ygIUF678y40"
  page_size = 48
  filename_template = "{{pub_date}}_{{title}}_{{id}}"
  update_period = "5h"
  format = "audio"
  quality = "low"
	# duration filters are in seconds
	# max_age is in days
	# min_age is in days
  filters = { title = "regex for title here", min_duration = 0, max_duration = 86400, max_age = 365, min_age = 1}
  playlist_sort = "desc"
  clean = { keep_last = 10 }
  [feeds.XYZ.custom]
  cover_art = "http://img"
  cover_art_quality = "high"
  category = "TV"
  subcategories = ["1", "2"]
  explicit = true
  lang = "en"
  author = "Mrs. Smith (mrs@smith.org)"
  ownerName = "Mrs. Smith"
  ownerEmail = "mrs@smith.org"
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	assert.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, "test/data/", config.Server.DataDir)
	assert.EqualValues(t, 80, config.Server.Port)

	assert.Equal(t, "/home/user/db/", config.Database.Dir)

	require.Len(t, config.Tokens["youtube"], 1)
	assert.Equal(t, "123", config.Tokens["youtube"][0])
	require.Len(t, config.Tokens["vimeo"], 2)
	assert.Equal(t, "321", config.Tokens["vimeo"][0])
	assert.Equal(t, "456", config.Tokens["vimeo"][1])

	assert.Len(t, config.Feeds, 1)
	feed, ok := config.Feeds["XYZ"]
	assert.True(t, ok)
	assert.Equal(t, "https://youtube.com/watch?v=ygIUF678y40", feed.URL)
	assert.EqualValues(t, 48, feed.PageSize)
	assert.EqualValues(t, "{{pub_date}}_{{title}}_{{id}}", feed.FilenameTemplate)
	assert.EqualValues(t, 5*time.Hour, feed.UpdatePeriod)
	assert.EqualValues(t, "audio", feed.Format)
	assert.EqualValues(t, "low", feed.Quality)
	assert.EqualValues(t, "regex for title here", feed.Filters.Title)
	assert.EqualValues(t, 0, feed.Filters.MinDuration)
	assert.EqualValues(t, 86400, feed.Filters.MaxDuration)
	assert.EqualValues(t, 365, feed.Filters.MaxAge)
	assert.EqualValues(t, 1, feed.Filters.MinAge)
	require.NotNil(t, feed.Clean)
	assert.EqualValues(t, 10, feed.Clean.KeepLast)
	assert.EqualValues(t, model.SortingDesc, feed.PlaylistSort)

	assert.EqualValues(t, "http://img", feed.Custom.CoverArt)
	assert.EqualValues(t, "high", feed.Custom.CoverArtQuality)
	assert.EqualValues(t, "TV", feed.Custom.Category)
	assert.True(t, feed.Custom.Explicit)
	assert.EqualValues(t, "en", feed.Custom.Language)
	assert.EqualValues(t, "Mrs. Smith (mrs@smith.org)", feed.Custom.Author)
	assert.EqualValues(t, "Mrs. Smith", feed.Custom.OwnerName)
	assert.EqualValues(t, "mrs@smith.org", feed.Custom.OwnerEmail)

	assert.EqualValues(t, feed.Custom.Subcategories, []string{"1", "2"})

	assert.Nil(t, config.Database.Badger)

	assert.True(t, config.Downloader.SelfUpdate)
	assert.EqualValues(t, 15, config.Downloader.Timeout)
}

func TestFilenameTemplateValidation(t *testing.T) {
	const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
  filename_template = "{{bad_token}}_{{id}}"
`
	path := setup(t, file)
	defer os.Remove(path)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid filename_template")
}

func TestCustomFormatExtensionValidation(t *testing.T) {
	t.Run("rejects invalid extension", func(t *testing.T) {
		const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
  format = "custom"
  [feeds.A.custom_format]
  extension = "../mp3"
`
		path := setup(t, file)
		defer os.Remove(path)

		_, err := LoadConfig(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid custom_format.extension")
	})

	t.Run("accepts normalized extension", func(t *testing.T) {
		const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
  format = "custom"
  [feeds.A.custom_format]
  extension = ".M4A"
`
		path := setup(t, file)
		defer os.Remove(path)

		_, err := LoadConfig(path)
		assert.NoError(t, err)
	})
}

func TestEnrichmentValidationIncludesConfigSection(t *testing.T) {
	const file = `
	[server]
	data_dir = "/data"

	[feeds]
	  [feeds.A]
	  url = "https://youtube.com/watch?v=ygIUF678y40"
	  [feeds.A.transcripts]
	    [[feeds.A.transcripts.stt]]
	    type = "openai"
	    model = "whisper-1"

	  [feeds.B]
	  url = "https://youtube.com/watch?v=ygIUF678y40"
	  [feeds.B.chapters]
	    image_max_width = -1
	`
	path := setup(t, file)
	defer os.Remove(path)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid feeds.A.transcripts stt provider 1")
	assert.Contains(t, err.Error(), "feeds.B.chapters image_max_width must be positive")
}

func TestLoadEmptyKeyList(t *testing.T) {
	const file = `
[tokens]
vimeo = []

[server]
data_dir = "/data"
[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	assert.NoError(t, err)
	require.NotNil(t, config)

	require.Len(t, config.Tokens, 1)
	require.Len(t, config.Tokens["vimeo"], 0)
}

func TestApplyDefaults(t *testing.T) {
	const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	assert.NoError(t, err)
	assert.NotNil(t, config)

	assert.Len(t, config.Feeds, 1)
	feed, ok := config.Feeds["A"]
	require.True(t, ok)

	assert.EqualValues(t, feed.UpdatePeriod, model.DefaultUpdatePeriod)
	assert.EqualValues(t, feed.PageSize, 50)
	assert.EqualValues(t, feed.Quality, "high")
	assert.EqualValues(t, feed.Custom.CoverArtQuality, "high")
	assert.EqualValues(t, feed.Format, "video")
}

func TestHttpServerListenAddress(t *testing.T) {
	const file = `
[server]
bind_address = "172.20.10.2"
port = 8080
path = "test"
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"

[database]
  badger = { truncate = true, file_io = true }
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	assert.NoError(t, err)
	require.NotNil(t, config)
	require.NotNil(t, config.Server.BindAddress)
	require.NotNil(t, config.Server.Path)
}

func TestDefaultHostname(t *testing.T) {
	cfg := Config{
		Server: web.Config{},
	}

	t.Run("empty hostname", func(t *testing.T) {
		cfg.applyDefaults("")
		assert.Equal(t, "http://localhost", cfg.Server.Hostname)
	})

	t.Run("empty hostname with port", func(t *testing.T) {
		cfg.Server.Hostname = ""
		cfg.Server.Port = 7979
		cfg.applyDefaults("")
		assert.Equal(t, "http://localhost:7979", cfg.Server.Hostname)
	})

	t.Run("skip overwrite", func(t *testing.T) {
		cfg.Server.Hostname = "https://my.host:4443"
		cfg.Server.Port = 80
		cfg.applyDefaults("")
		assert.Equal(t, "https://my.host:4443", cfg.Server.Hostname)
	})
}

func TestDefaultDatabasePath(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults("/home/user/podsync/config.toml")
	assert.Equal(t, "/home/user/podsync/db", cfg.Database.Dir)
}

func TestLoadBadgerConfig(t *testing.T) {
	const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"

[database]
  badger = { truncate = true, file_io = true }
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	assert.NoError(t, err)
	require.NotNil(t, config)
	require.NotNil(t, config.Database.Badger)

	assert.True(t, config.Database.Badger.Truncate)
	assert.True(t, config.Database.Badger.FileIO)
}

func TestGlobalCleanupPolicy(t *testing.T) {
	t.Run("global cleanup policy applied to feeds without cleanup", func(t *testing.T) {
		const file = `
[cleanup]
keep_last = 25

[server]
data_dir = "/data"

[feeds]
  [feeds.FEED1]
  url = "https://youtube.com/channel/test1"
  
  [feeds.FEED2]
  url = "https://youtube.com/channel/test2"
  clean = { keep_last = 5 }
`
		path := setup(t, file)
		defer os.Remove(path)

		config, err := LoadConfig(path)
		assert.NoError(t, err)
		require.NotNil(t, config)

		// Global cleanup policy should be set
		require.NotNil(t, config.Cleanup)
		assert.EqualValues(t, 25, config.Cleanup.KeepLast)

		// FEED1 should inherit global cleanup policy
		feed1, ok := config.Feeds["FEED1"]
		assert.True(t, ok)
		require.NotNil(t, feed1.Clean)
		assert.EqualValues(t, 25, feed1.Clean.KeepLast)

		// FEED2 should keep its own cleanup policy
		feed2, ok := config.Feeds["FEED2"]
		assert.True(t, ok)
		require.NotNil(t, feed2.Clean)
		assert.EqualValues(t, 5, feed2.Clean.KeepLast)
	})

	t.Run("no global cleanup policy", func(t *testing.T) {
		const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.FEED1]
  url = "https://youtube.com/channel/test1"
  
  [feeds.FEED2]
  url = "https://youtube.com/channel/test2"
  clean = { keep_last = 5 }
`
		path := setup(t, file)
		defer os.Remove(path)

		config, err := LoadConfig(path)
		assert.NoError(t, err)
		require.NotNil(t, config)

		// Global cleanup policy should not be set
		assert.Nil(t, config.Cleanup)

		// FEED1 should have no cleanup policy
		feed1, ok := config.Feeds["FEED1"]
		assert.True(t, ok)
		assert.Nil(t, feed1.Clean)

		// FEED2 should keep its own cleanup policy
		feed2, ok := config.Feeds["FEED2"]
		assert.True(t, ok)
		require.NotNil(t, feed2.Clean)
		assert.EqualValues(t, 5, feed2.Clean.KeepLast)
	})

	t.Run("feed cleanup overrides global cleanup", func(t *testing.T) {
		const file = `
[cleanup]
keep_last = 100

[server]
data_dir = "/data"

[feeds]
  [feeds.FEED1]
  url = "https://youtube.com/channel/test1"
  clean = { keep_last = 10 }
`
		path := setup(t, file)
		defer os.Remove(path)

		config, err := LoadConfig(path)
		assert.NoError(t, err)
		require.NotNil(t, config)

		// Global cleanup policy should be set
		require.NotNil(t, config.Cleanup)
		assert.EqualValues(t, 100, config.Cleanup.KeepLast)

		// FEED1 should use its own cleanup policy, not the global one
		feed1, ok := config.Feeds["FEED1"]
		assert.True(t, ok)
		require.NotNil(t, feed1.Clean)
		assert.EqualValues(t, 10, feed1.Clean.KeepLast)
	})
}

func TestEnvironmentVariables(t *testing.T) {
	t.Run("environment variables override config tokens", func(t *testing.T) {
		const file = `
[tokens]
youtube = "original_key"
vimeo = "original_vimeo_key"

[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
`
		path := setup(t, file)
		defer os.Remove(path)

		// Set environment variables
		t.Setenv("PODSYNC_YOUTUBE_API_KEY", "env_youtube_key")
		t.Setenv("PODSYNC_VIMEO_API_KEY", "env_vimeo_key")

		config, err := LoadConfig(path)
		assert.NoError(t, err)
		require.NotNil(t, config)

		// Environment variables should override config values
		require.Len(t, config.Tokens[model.ProviderYoutube], 1)
		assert.Equal(t, "env_youtube_key", config.Tokens[model.ProviderYoutube][0])

		require.Len(t, config.Tokens[model.ProviderVimeo], 1)
		assert.Equal(t, "env_vimeo_key", config.Tokens[model.ProviderVimeo][0])
	})

	t.Run("environment variables support multiple keys", func(t *testing.T) {
		const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
`
		path := setup(t, file)
		defer os.Remove(path)

		// Set environment variable with multiple keys
		t.Setenv("PODSYNC_YOUTUBE_API_KEY", "key1 key2 key3")

		config, err := LoadConfig(path)
		assert.NoError(t, err)
		require.NotNil(t, config)

		// Should parse multiple keys from environment variable
		assert.ElementsMatch(t, []string{"key1", "key2", "key3"}, config.Tokens[model.ProviderYoutube])
	})
}

func TestNoIndexConfig(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
`
		path := setup(t, file)
		defer os.Remove(path)

		config, err := LoadConfig(path)
		assert.NoError(t, err)
		require.NotNil(t, config)
		assert.False(t, config.Server.NoIndex)
	})

	t.Run("enabled when configured", func(t *testing.T) {
		const file = `
[server]
data_dir = "/data"
no_index = true

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40"
`
		path := setup(t, file)
		defer os.Remove(path)

		config, err := LoadConfig(path)
		assert.NoError(t, err)
		require.NotNil(t, config)
		assert.True(t, config.Server.NoIndex)
	})
}

func setup(t *testing.T, file string) string {
	t.Helper()

	f, err := os.CreateTemp("", "")
	require.NoError(t, err)

	defer f.Close()

	_, err = f.WriteString(file)
	require.NoError(t, err)

	return f.Name()
}

func TestEnrichmentConfig(t *testing.T) {
	const file = `
[server]
data_dir = "/data"

[tools]
video_to_chapters = "/opt/bin/video-to-chapters"

[transcripts]
languages = ["de", "en"]

[[transcripts.stt]]
type = "openai"
base_url = "https://api.openai.com/v1"
model = "whisper-1"

[chapters]
image_max_width = 640

[chapters.llm]
assemblyai_api_key = "aai"
gemini_api_key = "gem"

[feeds]
  [feeds.A]
  url = "https://youtube.com/channel/123"

  [feeds.B]
  url = "https://youtube.com/channel/456"
  [feeds.B.transcripts]
  enabled = false
  [feeds.B.chapters]
  fetch_video_for_audio = false
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	require.NoError(t, err)

	assert.Equal(t, "/opt/bin/video-to-chapters", config.Tools.VideoToChapters)

	// Feed A inherits the global sections
	feedA := config.Feeds["A"]
	require.NotNil(t, feedA.Transcripts)
	assert.True(t, feedA.Transcripts.IsEnabled())
	assert.Equal(t, []string{"de", "en"}, feedA.Transcripts.Languages)
	require.Len(t, feedA.Transcripts.STTProviders(), 1)
	assert.Equal(t, "openai", feedA.Transcripts.STTProviders()[0].Type)

	require.NotNil(t, feedA.Chapters)
	assert.True(t, feedA.Chapters.IsEnabled())
	assert.True(t, feedA.Chapters.ImagesEnabled())
	assert.True(t, feedA.Chapters.VideoFetchEnabled())
	assert.Equal(t, 640, feedA.Chapters.ImageWidth())
	assert.True(t, feedA.Chapters.LLMConfigured())

	// Feed B keeps its own overrides
	feedB := config.Feeds["B"]
	assert.False(t, feedB.Transcripts.IsEnabled())
	assert.True(t, feedB.Chapters.IsEnabled())
	assert.False(t, feedB.Chapters.VideoFetchEnabled())
	assert.False(t, feedB.Chapters.LLMConfigured(), "per-feed section does not inherit global LLM keys")
}

func TestEnrichmentConfigDefaults(t *testing.T) {
	const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/channel/123"
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	require.NoError(t, err)

	feedA := config.Feeds["A"]
	assert.Nil(t, feedA.Transcripts)
	assert.Nil(t, feedA.Chapters)

	// Nil sections behave as enabled with defaults
	assert.True(t, feedA.Transcripts.IsEnabled())
	assert.True(t, feedA.Chapters.IsEnabled())
	assert.True(t, feedA.Chapters.ImagesEnabled())
	assert.True(t, feedA.Chapters.VideoFetchEnabled())
	assert.Equal(t, 1280, feedA.Chapters.ImageWidth())
	assert.False(t, feedA.Chapters.LLMConfigured())
	assert.Empty(t, feedA.Transcripts.STTProviders())
}

func TestSTTProviderValidation(t *testing.T) {
	const file = `
[server]
data_dir = "/data"

[[transcripts.stt]]
type = "openai"

[feeds]
  [feeds.A]
  url = "https://youtube.com/channel/123"
`
	path := setup(t, file)
	defer os.Remove(path)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base_url is required")

	const badType = `
[server]
data_dir = "/data"

[[transcripts.stt]]
type = "bogus"

[feeds]
  [feeds.A]
  url = "https://youtube.com/channel/123"
`
	path2 := setup(t, badType)
	defer os.Remove(path2)

	_, err = LoadConfig(path2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown stt provider type")
}

func TestEnrichmentEnvironmentVariables(t *testing.T) {
	t.Setenv("PODSYNC_STT_API_KEY", "stt-secret")
	t.Setenv("PODSYNC_ASSEMBLYAI_API_KEY", "aai-secret")
	t.Setenv("PODSYNC_GEMINI_API_KEY", "gem-secret")

	const file = `
[server]
data_dir = "/data"

[[transcripts.stt]]
type = "openai"
base_url = "https://api.openai.com/v1"
model = "whisper-1"

[[transcripts.stt]]
type = "openai"
base_url = "https://other.example.com/v1"
model = "whisper-1"
api_key = "explicit"

[chapters]

[feeds]
  [feeds.A]
  url = "https://youtube.com/channel/123"
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	require.NoError(t, err)

	providers := config.Transcripts.STTProviders()
	require.Len(t, providers, 2)
	assert.Equal(t, "stt-secret", providers[0].APIKey, "env fills empty keys")
	assert.Equal(t, "explicit", providers[1].APIKey, "explicit keys win over env")

	require.NotNil(t, config.Chapters)
	assert.Equal(t, "aai-secret", config.Chapters.LLM.AssemblyAIKey)
	assert.Equal(t, "gem-secret", config.Chapters.LLM.GeminiKey)
	assert.True(t, config.Chapters.LLMConfigured())
}

func TestCustomLockedConfig(t *testing.T) {
	const file = `
[server]
data_dir = "/data"

[feeds]
  [feeds.A]
  url = "https://youtube.com/channel/123"
  [feeds.A.custom]
  locked = false
`
	path := setup(t, file)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	require.NoError(t, err)

	locked := config.Feeds["A"].Custom.Locked
	require.NotNil(t, locked)
	assert.False(t, *locked)
}
