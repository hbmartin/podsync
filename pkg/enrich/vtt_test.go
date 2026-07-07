package enrich

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertVTTToTranscriptJSON(t *testing.T) {
	vtt := `WEBVTT
Kind: captions
Language: en

NOTE this is a comment
and more comment

1
00:00:00.000 --> 00:00:03.500
<v Alice>Hello and welcome
to the show.

00:00:03.500 --> 00:00:07.000
Today we talk about <c.highlight>Go</c>.

00:00:07.000 --> 00:00:09.000
Today we talk about Go.

1:00:09.000 --> 1:00:12,000
Deep dive time.
`

	dir := t.TempDir()
	vttPath := filepath.Join(dir, "test.vtt")
	jsonPath := filepath.Join(dir, "test.transcript.json")
	require.NoError(t, os.WriteFile(vttPath, []byte(vtt), 0o600))

	require.NoError(t, ConvertVTTToTranscriptJSON(vttPath, jsonPath))

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)

	var doc TranscriptDoc
	require.NoError(t, json.Unmarshal(data, &doc))

	assert.Equal(t, "1.0.0", doc.Version)
	require.Len(t, doc.Segments, 3, "duplicate adjacent cue must be dropped")

	first := doc.Segments[0]
	assert.Equal(t, "Alice", first.Speaker)
	assert.Equal(t, 0.0, first.StartTime)
	assert.Equal(t, 3.5, first.EndTime)
	assert.Equal(t, "Hello and welcome to the show.", first.Body)

	second := doc.Segments[1]
	assert.Empty(t, second.Speaker)
	assert.Equal(t, "Today we talk about Go.", second.Body)

	third := doc.Segments[2]
	assert.Equal(t, 3609.0, third.StartTime)
	assert.Equal(t, 3612.0, third.EndTime)
	assert.Equal(t, "Deep dive time.", third.Body)
}

func TestConvertVTTToTranscriptJSONEmpty(t *testing.T) {
	dir := t.TempDir()
	vttPath := filepath.Join(dir, "empty.vtt")
	require.NoError(t, os.WriteFile(vttPath, []byte("WEBVTT\n"), 0o600))

	err := ConvertVTTToTranscriptJSON(vttPath, filepath.Join(dir, "out.json"))
	assert.Error(t, err)
}

func TestParseVTTTimestamp(t *testing.T) {
	assert.Equal(t, 3.5, parseVTTTimestamp("00:00:03.500"))
	assert.Equal(t, 3.5, parseVTTTimestamp("00:03.500"))
	assert.Equal(t, 3661.25, parseVTTTimestamp("01:01:01.250"))
	assert.Equal(t, 1.0, parseVTTTimestamp("00:01,000"))
}
