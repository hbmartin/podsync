// Package enrich generates transcript and chapter sidecar files for
// downloaded episodes, using platform data first and optional external
// helpers (speech-to-text providers, chapter tools) as fallbacks.
package enrich

import "fmt"

// Sidecar file names are all derived from the episode's base file name so
// they live next to the media file and are trivially discoverable by the
// web server, cleanup and feed generation.

// TranscriptVTTName is the name of the WebVTT transcript sidecar.
func TranscriptVTTName(baseName string) string {
	return baseName + ".vtt"
}

// TranscriptJSONName is the name of the PodcastIndex JSON transcript sidecar.
func TranscriptJSONName(baseName string) string {
	return baseName + ".transcript.json"
}

// ChaptersJSONName is the name of the PodcastIndex chapters JSON sidecar.
func ChaptersJSONName(baseName string) string {
	return baseName + ".chapters.json"
}

// ChapterImageName is the name of the frame image for chapter idx (0-based)
// out of total. Numbering is 1-based and zero-padded for stable sorting.
func ChapterImageName(baseName string, idx, total int) string {
	width := 2
	for limit := 100; total >= limit; limit *= 10 {
		width++
	}
	return fmt.Sprintf("%s.chapter-%0*d.jpg", baseName, width, idx+1)
}
