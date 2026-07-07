package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/model"
)

func setupInput(answers ...string) *strings.Reader {
	return strings.NewReader(strings.Join(answers, "\n") + "\n")
}

func TestSetupGeneratesLoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	input := setupInput(
		"https://www.youtube.com/channel/UC_channel_id", // feed URL
		"mychannel",                    // feed ID
		"audio",                        // format
		"low",                          // quality
		"n",                            // add another feed?
		"/tmp/podsync-data",            // data dir
		"9090",                         // port
		"https://podcasts.example.com", // hostname
		"test-api-key",                 // YouTube API key
	)

	var output bytes.Buffer
	require.NoError(t, runSetup(path, input, &output))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	require.Len(t, cfg.Feeds, 1)
	f := cfg.Feeds["mychannel"]
	require.NotNil(t, f)
	assert.Equal(t, "https://www.youtube.com/channel/UC_channel_id", f.URL)
	assert.Equal(t, model.FormatAudio, f.Format)
	assert.Equal(t, model.QualityLow, f.Quality)

	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, "https://podcasts.example.com", cfg.Server.Hostname)
	assert.Equal(t, "local", cfg.Storage.Type)
	assert.Equal(t, "/tmp/podsync-data", cfg.Storage.Local.DataDir)
	assert.Equal(t, []string{"test-api-key"}, []string(cfg.Tokens[model.ProviderYoutube]))

	// Defaults from LoadConfig still apply
	assert.Equal(t, model.DefaultPageSize, f.PageSize)
	assert.Equal(t, model.DefaultUpdatePeriod, f.UpdatePeriod)

	// The file contains an API key, so it must not be world-readable
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSetupDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	input := setupInput(
		"https://www.youtube.com/@somehandle", // feed URL
		"",                                    // feed ID → feed1
		"",                                    // format → video
		"",                                    // quality → high
		"",                                    // add another feed? → n
		"",                                    // data dir → ./data
		"",                                    // port → 8080
		"",                                    // hostname → http://localhost:8080
		"",                                    // YouTube API key → empty
	)

	var output bytes.Buffer
	require.NoError(t, runSetup(path, input, &output))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	f := cfg.Feeds["feed1"]
	require.NotNil(t, f)
	assert.Equal(t, model.FormatVideo, f.Format)
	assert.Equal(t, model.QualityHigh, f.Quality)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, "http://localhost:8080", cfg.Server.Hostname)
	assert.Equal(t, "./data", cfg.Storage.Local.DataDir)
	assert.NotContains(t, cfg.Tokens, model.ProviderYoutube)

	// The commented API key pointer must be present in the generated file
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), youtubeAPIKeyURL)
}

func TestSetupMultipleFeeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	input := setupInput(
		"https://www.youtube.com/playlist?list=PL123", // feed 1 URL
		"tutorials", "video", "high",
		"y",                              // add another feed
		"https://www.twitch.tv/streamer", // feed 2 URL
		"streams", "video", "low",
		"n", // no more feeds
		"",  // data dir
		"",  // port
		"",  // hostname
		"",  // YouTube API key
	)

	var output bytes.Buffer
	require.NoError(t, runSetup(path, input, &output))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	require.Len(t, cfg.Feeds, 2)
	require.NotNil(t, cfg.Feeds["tutorials"])
	require.NotNil(t, cfg.Feeds["streams"])
	assert.Equal(t, "https://www.twitch.tv/streamer", cfg.Feeds["streams"].URL)

	// Twitch token hint must be present
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "CLIENT_ID:CLIENT_SECRET")
}

func TestSetupRepromptsOnInvalidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	input := setupInput(
		"not a url",                        // invalid URL → re-prompt
		"https://vimeo.com/channels/staff", // valid URL
		"bad id!",                          // invalid feed ID → re-prompt
		"staffpicks",                       // valid feed ID
		"maybe",                            // invalid format → re-prompt
		"video",                            // valid format
		"high",                             // quality
		"n",                                // no more feeds
		"",                                 // data dir
		"70000",                            // invalid port → re-prompt
		"8081",                             // valid port
		"",                                 // hostname
	)

	var output bytes.Buffer
	require.NoError(t, runSetup(path, input, &output))

	out := output.String()
	assert.Contains(t, out, "Unsupported URL")
	assert.Contains(t, out, "Feed ID may contain only")
	assert.Contains(t, out, "Please enter one of")
	assert.Contains(t, out, "port number between")

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Feeds["staffpicks"])
	assert.Equal(t, 8081, cfg.Server.Port)

	// Vimeo-only feed set: no YouTube API key prompt
	assert.NotContains(t, out, "YouTube API key")
	// But the generated file points at the Vimeo token docs
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "vimeo")
}

func TestSetupRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("# precious existing config\n")
	require.NoError(t, os.WriteFile(path, original, 0o644))

	var output bytes.Buffer
	err := runSetup(path, setupInput("n"), &output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborted")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, data, "existing file must be left untouched")
}

func TestSetupOverwritesWhenConfirmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("# old config\n"), 0o644))

	input := setupInput(
		"y", // overwrite?
		"https://soundcloud.com/artist/sets/album",
		"music", "audio", "high",
		"n",
		"", "", "",
	)

	var output bytes.Buffer
	require.NoError(t, runSetup(path, input, &output))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Feeds["music"])
}

func TestSetupFailsOnEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	// Input ends before all questions are answered
	input := strings.NewReader("https://www.youtube.com/@somehandle\n")

	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runSetup(path, input, &output)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("setup must fail on EOF instead of hanging")
	}

	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "no config file must be written on failure")
}
