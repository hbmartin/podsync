package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
	"github.com/mxpv/podsync/services/web"
)

type Config struct {
	// Server is the web server configuration
	Server web.Config `toml:"server"`
	// S3 is the optional configuration for S3-compatible storage provider
	Storage fs.Config `toml:"storage"`
	// Log is the optional logging configuration
	Log Log `toml:"log"`
	// Database configuration
	Database db.Config `toml:"database"`
	// Feeds is a list of feeds to host by this app.
	// ID will be used as feed ID in http://podsync.net/{FEED_ID}.xml
	Feeds map[string]*feed.Config
	// Tokens is API keys to use to access YouTube/Vimeo APIs.
	Tokens map[model.Provider]StringSlice `toml:"tokens"`
	// Downloader (youtube-dl) configuration
	Downloader ytdl.Config `toml:"downloader"`
	// Global cleanup policy applied to feeds that don't specify their own cleanup policy
	Cleanup *feed.Cleanup `toml:"cleanup"`
	// Tools points to the optional external helper binaries used for
	// transcript and chapter processing
	Tools feed.ToolsConfig `toml:"tools"`
	// Transcripts is the global transcript policy, applied to feeds that
	// don't specify their own [feeds.X.transcripts] section
	Transcripts *feed.TranscriptsConfig `toml:"transcripts"`
	// Chapters is the global chapter policy, applied to feeds that don't
	// specify their own [feeds.X.chapters] section
	Chapters *feed.ChaptersConfig `toml:"chapters"`
}

type Log struct {
	// Filename to write the log to (instead of stdout)
	Filename string `toml:"filename"`
	// MaxSize is the maximum size of the log file in MB
	MaxSize int `toml:"max_size"`
	// MaxBackups is the maximum number of log file backups to keep after rotation
	MaxBackups int `toml:"max_backups"`
	// MaxAge is the maximum number of days to keep the logs for
	MaxAge int `toml:"max_age"`
	// Compress old backups
	Compress bool `toml:"compress"`
	// Debug mode
	Debug bool `toml:"debug"`
}

// LoadConfig loads TOML configuration from a file path
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read config file: %s", path)
	}

	config := Config{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal toml")
	}

	for id, f := range config.Feeds {
		f.ID = id
	}

	config.applyDefaults(path)
	config.applyEnv()

	if err := config.validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

func (c *Config) validate() error {
	var result *multierror.Error

	if c.Server.DataDir != "" {
		log.Warnf(`server.data_dir is deprecated, and will be removed in a future release. Use the following config instead:

[storage]
  [storage.local]
  data_dir = "%s"

`, c.Server.DataDir)
		if c.Storage.Local.DataDir == "" {
			c.Storage.Local.DataDir = c.Server.DataDir
		}
	}

	if c.Server.Path != "" {
		var pathReg = regexp.MustCompile(model.PathRegex)
		if !pathReg.MatchString(c.Server.Path) {
			result = multierror.Append(result, errors.Errorf("Server handle path must be match %s or empty", model.PathRegex))
		}
	}

	switch c.Storage.Type {
	case "local":
		if c.Storage.Local.DataDir == "" {
			result = multierror.Append(result, errors.New("data directory is required for local storage"))
		}
	case "s3":
		if c.Storage.S3.EndpointURL == "" || c.Storage.S3.Region == "" || c.Storage.S3.Bucket == "" {
			result = multierror.Append(result, errors.New("S3 storage requires endpoint_url, region and bucket to be set"))
		}
	default:
		result = multierror.Append(result, errors.Errorf("unknown storage type: %s", c.Storage.Type))
	}

	if len(c.Feeds) == 0 {
		result = multierror.Append(result, errors.New("at least one feed must be specified"))
	}

	for id, f := range c.Feeds {
		if f.URL == "" {
			result = multierror.Append(result, errors.Errorf("URL is required for %q", id))
		}
		if err := feed.ValidateFilenameTemplate(f.FilenameTemplate); err != nil {
			result = multierror.Append(result, errors.Wrapf(err, "invalid filename_template for %q", id))
		}
		if f.Format == model.FormatCustom {
			if err := feed.ValidateCustomExtension(f.CustomFormat.Extension); err != nil {
				result = multierror.Append(result, errors.Wrapf(err, "invalid custom_format.extension for %q", id))
			}
		}
	}

	for _, transcripts := range c.transcriptConfigRefs() {
		for i, provider := range transcripts.config.STTProviders() {
			if err := validateSTTProvider(provider); err != nil {
				result = multierror.Append(result, errors.Wrapf(err, "invalid %s stt provider %d", transcripts.label, i+1))
			}
		}
	}

	for _, chapters := range c.chapterConfigRefs() {
		if chapters.config.ImageMaxWidth < 0 {
			result = multierror.Append(result, errors.Errorf("%s image_max_width must be positive", chapters.label))
		}
	}

	return result.ErrorOrNil()
}

func validateSTTProvider(provider *feed.STTProviderConfig) error {
	switch provider.Type {
	case feed.STTTypeOpenAI:
		if provider.BaseURL == "" {
			return errors.New("base_url is required for openai providers")
		}
		if provider.Model == "" {
			return errors.New("model is required for openai providers")
		}
	case feed.STTTypeWhisperCPP:
		if provider.Binary == "" {
			return errors.New("binary is required for whisper_cpp providers")
		}
		if provider.ModelPath == "" {
			return errors.New("model_path is required for whisper_cpp providers")
		}
	case feed.STTTypeCommand:
		if len(provider.Command) == 0 {
			return errors.New("command is required for command providers")
		}
	default:
		return errors.Errorf("unknown stt provider type %q (expected openai, whisper_cpp or command)", provider.Type)
	}
	return nil
}

func (c *Config) applyDefaults(configPath string) {
	if c.Server.Hostname == "" {
		if c.Server.Port != 0 && c.Server.Port != 80 {
			c.Server.Hostname = fmt.Sprintf("http://localhost:%d", c.Server.Port)
		} else {
			c.Server.Hostname = "http://localhost"
		}
	}

	if c.Storage.Type == "" {
		c.Storage.Type = "local"
	}

	if c.Log.Filename != "" {
		if c.Log.MaxSize == 0 {
			c.Log.MaxSize = model.DefaultLogMaxSize
		}
		if c.Log.MaxAge == 0 {
			c.Log.MaxAge = model.DefaultLogMaxAge
		}
		if c.Log.MaxBackups == 0 {
			c.Log.MaxBackups = model.DefaultLogMaxBackups
		}
	}

	if c.Database.Dir == "" {
		c.Database.Dir = filepath.Join(filepath.Dir(configPath), "db")
	}

	for _, _feed := range c.Feeds {
		if _feed.UpdatePeriod == 0 {
			_feed.UpdatePeriod = model.DefaultUpdatePeriod
		}

		if _feed.Quality == "" {
			_feed.Quality = model.DefaultQuality
		}

		if _feed.Custom.CoverArtQuality == "" {
			_feed.Custom.CoverArtQuality = model.DefaultQuality
		}

		if _feed.Format == "" {
			_feed.Format = model.DefaultFormat
		}

		if _feed.PageSize == 0 {
			_feed.PageSize = model.DefaultPageSize
		}

		if _feed.PlaylistSort == "" {
			_feed.PlaylistSort = model.SortingAsc
		}

		// Apply global cleanup policy if feed doesn't have its own
		if _feed.Clean == nil && c.Cleanup != nil {
			_feed.Clean = c.Cleanup
		}

		// Apply global transcript/chapter policies if the feed doesn't
		// have its own
		if _feed.Transcripts == nil {
			_feed.Transcripts = c.Transcripts
		}
		if _feed.Chapters == nil {
			_feed.Chapters = c.Chapters
		}
	}
}

func (c *Config) applyEnv() {
	envVars := map[model.Provider]string{
		model.ProviderYoutube:    "PODSYNC_YOUTUBE_API_KEY",
		model.ProviderVimeo:      "PODSYNC_VIMEO_API_KEY",
		model.ProviderSoundcloud: "PODSYNC_SOUNDCLOUD_API_KEY",
		model.ProviderTwitch:     "PODSYNC_TWITCH_API_KEY",
	}

	// Replace API keys from config with environment variables
	for provider, envVar := range envVars {
		val, ok := os.LookupEnv(envVar)
		if ok {
			log.Infof("Found %s environment variable, replacing config token with it", envVar)
			// If no tokens are provided in the config.toml, we need to create a new map
			if c.Tokens == nil {
				c.Tokens = make(map[model.Provider]StringSlice)
			}
			// Support multiple keys separated by spaces for API key rotation
			keys := strings.Fields(val)
			c.Tokens[provider] = keys
		}
	}

	// STT API key applies to "openai" providers that don't set their own
	if sttKey, ok := os.LookupEnv("PODSYNC_STT_API_KEY"); ok {
		for _, transcripts := range c.transcriptConfigs() {
			for _, provider := range transcripts.STTProviders() {
				if provider.Type == feed.STTTypeOpenAI && provider.APIKey == "" {
					provider.APIKey = sttKey
				}
			}
		}
	}

	// LLM chapter generation keys
	for _, chapters := range c.chapterConfigs() {
		if key, ok := os.LookupEnv("PODSYNC_ASSEMBLYAI_API_KEY"); ok && chapters.LLM.AssemblyAIKey == "" {
			chapters.LLM.AssemblyAIKey = key
		}
		if key, ok := os.LookupEnv("PODSYNC_GEMINI_API_KEY"); ok && chapters.LLM.GeminiKey == "" {
			chapters.LLM.GeminiKey = key
		}
	}
}

// transcriptConfigs returns all distinct transcript config sections
// (global + per-feed overrides).
func (c *Config) transcriptConfigs() []*feed.TranscriptsConfig {
	refs := c.transcriptConfigRefs()
	configs := make([]*feed.TranscriptsConfig, 0, len(refs))
	for _, ref := range refs {
		configs = append(configs, ref.config)
	}
	return configs
}

// chapterConfigs returns all distinct chapter config sections
// (global + per-feed overrides).
func (c *Config) chapterConfigs() []*feed.ChaptersConfig {
	refs := c.chapterConfigRefs()
	configs := make([]*feed.ChaptersConfig, 0, len(refs))
	for _, ref := range refs {
		configs = append(configs, ref.config)
	}
	return configs
}

type transcriptConfigRef struct {
	label  string
	config *feed.TranscriptsConfig
}

func (c *Config) transcriptConfigRefs() []transcriptConfigRef {
	seen := make(map[*feed.TranscriptsConfig]bool)
	var configs []transcriptConfigRef
	add := func(label string, cfg *feed.TranscriptsConfig) {
		if cfg != nil && !seen[cfg] {
			seen[cfg] = true
			configs = append(configs, transcriptConfigRef{label: label, config: cfg})
		}
	}
	add("transcripts", c.Transcripts)
	for id, f := range c.Feeds {
		add(fmt.Sprintf("feeds.%s.transcripts", id), f.Transcripts)
	}
	return configs
}

type chapterConfigRef struct {
	label  string
	config *feed.ChaptersConfig
}

func (c *Config) chapterConfigRefs() []chapterConfigRef {
	seen := make(map[*feed.ChaptersConfig]bool)
	var configs []chapterConfigRef
	add := func(label string, cfg *feed.ChaptersConfig) {
		if cfg != nil && !seen[cfg] {
			seen[cfg] = true
			configs = append(configs, chapterConfigRef{label: label, config: cfg})
		}
	}
	add("chapters", c.Chapters)
	for id, f := range c.Feeds {
		add(fmt.Sprintf("feeds.%s.chapters", id), f.Chapters)
	}
	return configs
}

// StringSlice is a toml extension that lets you to specify either a string
// value (a slice with just one element) or a string slice.
type StringSlice []string

func (s *StringSlice) UnmarshalTOML(v interface{}) error {
	if str, ok := v.(string); ok {
		*s = []string{str}
		return nil
	}

	return errors.New("failed to decode string slice field")
}
