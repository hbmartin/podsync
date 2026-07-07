package stt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
)

func TestNewChainValidation(t *testing.T) {
	_, err := NewChain([]*feed.STTProviderConfig{{Type: "bogus"}}, "")
	assert.Error(t, err)

	_, err = NewChain([]*feed.STTProviderConfig{{Type: feed.STTTypeOpenAI}}, "")
	assert.Error(t, err, "openai requires base_url and model")

	_, err = NewChain([]*feed.STTProviderConfig{{Type: feed.STTTypeCommand}}, "")
	assert.Error(t, err, "command requires a command")

	_, err = NewChain([]*feed.STTProviderConfig{{Type: feed.STTTypeWhisperCPP, Binary: "whisper-cli"}}, "ffmpeg")
	assert.Error(t, err, "whisper_cpp requires a model path")

	chain, err := NewChain([]*feed.STTProviderConfig{
		{Type: feed.STTTypeOpenAI, BaseURL: "http://localhost:9000/v1", Model: "whisper-1"},
		{Type: feed.STTTypeCommand, Command: []string{"true"}},
	}, "")
	require.NoError(t, err)
	require.Len(t, chain, 2)
	assert.Equal(t, "openai", chain[0].Name())
	assert.Equal(t, "command", chain[1].Name())
}

func TestOpenAITranscribeVTT(t *testing.T) {
	const vttBody = "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/audio/transcriptions", r.URL.Path)
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))

		require.NoError(t, r.ParseMultipartForm(1<<20))
		assert.Equal(t, "whisper-1", r.FormValue("model"))
		assert.Equal(t, "vtt", r.FormValue("response_format"))
		assert.Equal(t, "en", r.FormValue("language"))

		file, header, err := r.FormFile("file")
		require.NoError(t, err)
		defer file.Close()
		assert.Equal(t, "audio.mp3", header.Filename)
		data, err := io.ReadAll(file)
		require.NoError(t, err)
		assert.Equal(t, "fake audio", string(data))

		_, _ = w.Write([]byte(vttBody))
	}))
	defer server.Close()

	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "audio.mp3")
	require.NoError(t, os.WriteFile(mediaPath, []byte("fake audio"), 0o600))

	provider, err := newOpenAI(&feed.STTProviderConfig{
		Type:    feed.STTTypeOpenAI,
		BaseURL: server.URL + "/v1",
		APIKey:  "secret",
		Model:   "whisper-1",
	}, DefaultTimeout)
	require.NoError(t, err)

	outPath := filepath.Join(dir, "out.vtt")
	require.NoError(t, provider.Transcribe(context.Background(), mediaPath, "en", outPath))

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, vttBody, string(data))
}

func TestOpenAITranscribeJSONFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text": "Hello world", "segments": [
			{"start": 0.0, "end": 2.5, "text": " Hello"},
			{"start": 2.5, "end": 5.0, "text": "world"}
		]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "audio.mp3")
	require.NoError(t, os.WriteFile(mediaPath, []byte("fake audio"), 0o600))

	provider, err := newOpenAI(&feed.STTProviderConfig{
		BaseURL: server.URL,
		Model:   "whisper-1",
	}, DefaultTimeout)
	require.NoError(t, err)

	outPath := filepath.Join(dir, "out.vtt")
	require.NoError(t, provider.Transcribe(context.Background(), mediaPath, "", outPath))

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "WEBVTT")
	assert.Contains(t, string(data), "00:00:00.000 --> 00:00:02.500")
	assert.Contains(t, string(data), "Hello")
}

func TestOpenAITranscribeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error": "rate limited"}`, http.StatusTooManyRequests)
	}))
	defer server.Close()

	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "audio.mp3")
	require.NoError(t, os.WriteFile(mediaPath, []byte("fake audio"), 0o600))

	provider, err := newOpenAI(&feed.STTProviderConfig{BaseURL: server.URL, Model: "m"}, DefaultTimeout)
	require.NoError(t, err)

	err = provider.Transcribe(context.Background(), mediaPath, "", filepath.Join(dir, "out.vtt"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

func TestCommandProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}

	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "audio.mp3")
	require.NoError(t, os.WriteFile(mediaPath, []byte("fake audio"), 0o600))
	outPath := filepath.Join(dir, "out.vtt")

	provider, err := newCommand(&feed.STTProviderConfig{
		Type:    feed.STTTypeCommand,
		Command: []string{`printf 'WEBVTT lang=%s file=%s' "$PODSYNC_LANGUAGE" "$PODSYNC_AUDIO_FILE" > "$PODSYNC_TRANSCRIPT_OUTPUT"`},
	})
	require.NoError(t, err)

	require.NoError(t, provider.Transcribe(context.Background(), mediaPath, "en", outPath))

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, "WEBVTT lang=en file="+mediaPath, string(data))
}

func TestCommandProviderNoOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}

	dir := t.TempDir()
	provider, err := newCommand(&feed.STTProviderConfig{Command: []string{"true"}})
	require.NoError(t, err)

	err = provider.Transcribe(context.Background(), "in.mp3", "", filepath.Join(dir, "out.vtt"))
	assert.Error(t, err)
}

func TestTranscribeChainFallsThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.vtt")

	chain, err := NewChain([]*feed.STTProviderConfig{
		{Type: feed.STTTypeCommand, Command: []string{"false"}}, // always fails
		{Type: feed.STTTypeCommand, Command: []string{`printf WEBVTT > "$PODSYNC_TRANSCRIPT_OUTPUT"`}},
	}, "")
	require.NoError(t, err)

	provider, err := Transcribe(context.Background(), chain, "in.mp3", "en", outPath)
	require.NoError(t, err)
	assert.Equal(t, "command", provider)
	assert.FileExists(t, outPath)
}

func TestTranscribeChainAllFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}

	chain, err := NewChain([]*feed.STTProviderConfig{
		{Type: feed.STTTypeCommand, Command: []string{"false"}},
	}, "")
	require.NoError(t, err)

	_, err = Transcribe(context.Background(), chain, "in.mp3", "en", filepath.Join(t.TempDir(), "out.vtt"))
	assert.Error(t, err)
}

func TestTranscribeEmptyChain(t *testing.T) {
	_, err := Transcribe(context.Background(), nil, "in.mp3", "en", "out.vtt")
	assert.Error(t, err)
}
