package feed

// ToolsConfig points to the optional external helper binaries used for
// chapter processing. Values default to the bare command names, resolved via
// PATH. Missing tools disable the corresponding functionality with a logged
// warning instead of failing.
//
// Transcript conversion (VTT to PodcastIndex JSON) and description chapter
// parsing are handled in-process by the
// github.com/hbmartin/podcast-rss-generator/v2 transcript and chapters
// packages, so they have no external tool configuration.
type ToolsConfig struct {
	// VideoToChapters is the CLI of hbmartin/video-to-chapters-with-transcript,
	// used to generate chapters with an LLM when a video has none.
	VideoToChapters string `toml:"video_to_chapters"`
	// FFmpeg binary used for chapter frame extraction and MP4 chapter remuxing.
	FFmpeg string `toml:"ffmpeg"`
}

// ApplyDefaults fills unset tool paths with the default command names.
func (c *ToolsConfig) ApplyDefaults() {
	if c.VideoToChapters == "" {
		c.VideoToChapters = "video-to-chapters-with-transcript"
	}
	if c.FFmpeg == "" {
		c.FFmpeg = "ffmpeg"
	}
}

// TranscriptsConfig controls transcript downloading and generation.
type TranscriptsConfig struct {
	// Enabled toggles transcript support. Unset means enabled.
	Enabled *bool `toml:"enabled"`
	// Languages is the subtitle language preference list (e.g. ["en", "de"]).
	// When empty, the feed's custom.lang is used, falling back to "en".
	Languages []string `toml:"languages"`
	// STT is an ordered chain of speech-to-text fallback providers used when
	// the platform has no subtitles at all. Empty disables STT fallback.
	STT []*STTProviderConfig `toml:"stt"`
}

// IsEnabled reports whether transcripts are enabled (default true).
func (c *TranscriptsConfig) IsEnabled() bool {
	return c == nil || c.Enabled == nil || *c.Enabled
}

// STTProviders returns the configured STT fallback chain, if any.
func (c *TranscriptsConfig) STTProviders() []*STTProviderConfig {
	if c == nil {
		return nil
	}
	return c.STT
}

// STT provider types.
const (
	STTTypeOpenAI     = "openai"
	STTTypeWhisperCPP = "whisper_cpp"
	STTTypeCommand    = "command"
)

// STTProviderConfig configures one speech-to-text provider in the fallback chain.
type STTProviderConfig struct {
	// Type is one of "openai", "whisper_cpp" or "command".
	Type string `toml:"type"`

	// BaseURL of an OpenAI-compatible API (e.g. https://api.openai.com/v1);
	// the provider POSTs to {base_url}/audio/transcriptions. Type "openai" only.
	BaseURL string `toml:"base_url"`
	// APIKey for the OpenAI-compatible API. May also come from the
	// PODSYNC_STT_API_KEY environment variable. Type "openai" only.
	APIKey string `toml:"api_key"`
	// Model name, e.g. "whisper-1". Type "openai" only.
	Model string `toml:"model"`

	// Binary is the whisper.cpp CLI path or name. Type "whisper_cpp" only.
	Binary string `toml:"binary"`
	// ModelPath is the ggml/gguf model file for whisper.cpp. Type "whisper_cpp" only.
	ModelPath string `toml:"model_path"`

	// Command to execute for type "command". It receives the environment
	// variables PODSYNC_AUDIO_FILE, PODSYNC_TRANSCRIPT_OUTPUT and
	// PODSYNC_LANGUAGE, and must write a WebVTT file to the output path.
	Command []string `toml:"command"`

	// Timeout in seconds for this provider (default 1800).
	Timeout int `toml:"timeout"`
}

// ChaptersConfig controls chapter discovery, generation and images.
type ChaptersConfig struct {
	// Enabled toggles chapter support. Unset means enabled.
	Enabled *bool `toml:"enabled"`
	// ExtractImages toggles per-chapter frame extraction. Unset means enabled.
	ExtractImages *bool `toml:"extract_images"`
	// FetchVideoForAudio allows downloading a temporary low-resolution video
	// for audio feeds when frames or LLM chapter generation need it.
	// Unset means enabled.
	FetchVideoForAudio *bool `toml:"fetch_video_for_audio"`
	// ImageMaxWidth bounds extracted frame width in pixels (default 1280).
	ImageMaxWidth int `toml:"image_max_width"`
	// LLM configures AI chapter generation, which activates automatically
	// when both API keys are present.
	LLM LLMConfig `toml:"llm"`
}

// IsEnabled reports whether chapters are enabled (default true).
func (c *ChaptersConfig) IsEnabled() bool {
	return c == nil || c.Enabled == nil || *c.Enabled
}

// ImagesEnabled reports whether chapter frame extraction is enabled (default true).
func (c *ChaptersConfig) ImagesEnabled() bool {
	return c == nil || c.ExtractImages == nil || *c.ExtractImages
}

// VideoFetchEnabled reports whether fetching a temporary video for audio
// feeds is allowed (default true).
func (c *ChaptersConfig) VideoFetchEnabled() bool {
	return c == nil || c.FetchVideoForAudio == nil || *c.FetchVideoForAudio
}

// ImageWidth returns the maximum chapter image width (default 1280).
func (c *ChaptersConfig) ImageWidth() int {
	if c == nil || c.ImageMaxWidth <= 0 {
		return 1280
	}
	return c.ImageMaxWidth
}

// LLMConfigured reports whether LLM chapter generation can run.
func (c *ChaptersConfig) LLMConfigured() bool {
	return c != nil && c.LLM.Configured()
}

// LLMSettings returns the LLM configuration (zero value when unset).
func (c *ChaptersConfig) LLMSettings() LLMConfig {
	if c == nil {
		return LLMConfig{}
	}
	return c.LLM
}

// LLMConfig holds the API keys required by video-to-chapters-with-transcript.
type LLMConfig struct {
	// AssemblyAIKey may also come from PODSYNC_ASSEMBLYAI_API_KEY.
	AssemblyAIKey string `toml:"assemblyai_api_key"`
	// GeminiKey may also come from PODSYNC_GEMINI_API_KEY.
	GeminiKey string `toml:"gemini_api_key"`
}

// Configured reports whether LLM chapter generation can run.
func (c LLMConfig) Configured() bool {
	return c.AssemblyAIKey != "" && c.GeminiKey != ""
}
