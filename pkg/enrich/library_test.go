package enrich

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertTranscriptUsesLibrary verifies the VTT to PodcastIndex JSON
// conversion is handled in-process by the podcast-rss-generator transcript
// package for well-formed WebVTT input.
func TestConvertTranscriptUsesLibrary(t *testing.T) {
	const vtt = `WEBVTT

00:00:00.000 --> 00:00:02.000
<v Alice>Hello and welcome.

00:00:02.000 --> 00:00:04.000
Today we dive in.
`
	dir := t.TempDir()
	vttPath := filepath.Join(dir, "ep.vtt")
	jsonPath := filepath.Join(dir, "ep.transcript.json")
	require.NoError(t, os.WriteFile(vttPath, []byte(vtt), 0o600))

	require.NoError(t, convertTranscript(vttPath, jsonPath))

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)

	var doc struct {
		Version  string           `json:"version"`
		Segments []map[string]any `json:"segments"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, "1.0.0", doc.Version)
	require.Len(t, doc.Segments, 2)
	assert.Equal(t, "Alice", doc.Segments[0]["speaker"])
	assert.Equal(t, "Hello and welcome.", doc.Segments[0]["body"])
}

// TestConvertTranscriptFallsBackToBuiltin verifies that when the library
// rejects the input (here, a VTT file missing its WEBVTT header) the built-in
// converter still produces a transcript.
func TestConvertTranscriptFallsBackToBuiltin(t *testing.T) {
	// No "WEBVTT" header: the library's parser rejects this, but the built-in
	// parser is lenient and still extracts the cue.
	const headerless = `00:00:00.000 --> 00:00:02.000
Hello world
`
	dir := t.TempDir()
	vttPath := filepath.Join(dir, "ep.vtt")
	jsonPath := filepath.Join(dir, "ep.transcript.json")
	require.NoError(t, os.WriteFile(vttPath, []byte(headerless), 0o600))

	require.NoError(t, convertTranscript(vttPath, jsonPath))

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)

	var doc struct {
		Segments []map[string]any `json:"segments"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	require.Len(t, doc.Segments, 1)
	assert.Equal(t, "Hello world", doc.Segments[0]["body"])
}

// TestLibraryDescriptionChapters verifies description timestamps are parsed by
// the podcast-rss-generator chapters package and mapped onto the local
// Chapter shape.
func TestLibraryDescriptionChapters(t *testing.T) {
	chapters := libraryDescriptionChapters("00:00 Intro\n05:00 Deep Dive\n10:30 Wrap Up")
	require.Len(t, chapters, 3)
	assert.Equal(t, Chapter{StartTime: 0, Title: "Intro"}, chapters[0])
	assert.Equal(t, Chapter{StartTime: 300, Title: "Deep Dive"}, chapters[1])
	assert.Equal(t, Chapter{StartTime: 630, Title: "Wrap Up"}, chapters[2])
}

// TestLibraryDescriptionChaptersNone verifies that a description without a
// usable chapter list yields no chapters (so the caller can fall through).
func TestLibraryDescriptionChaptersNone(t *testing.T) {
	assert.Nil(t, libraryDescriptionChapters("Just a plain description with no timestamps."))
	assert.Nil(t, libraryDescriptionChapters("00:00 Only one marker"))
}
