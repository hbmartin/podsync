package enrich

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// TranscriptSegment is one cue of a PodcastIndex JSON transcript.
// See https://github.com/Podcastindex-org/podcast-namespace/blob/main/transcripts/transcripts.md
type TranscriptSegment struct {
	Speaker   string  `json:"speaker,omitempty"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
	Body      string  `json:"body"`
}

// TranscriptDoc is a PodcastIndex JSON transcript document.
type TranscriptDoc struct {
	Version  string              `json:"version"`
	Segments []TranscriptSegment `json:"segments"`
}

var (
	vttTimingPattern = regexp.MustCompile(`^(\d{1,2}:)?\d{1,2}:\d{2}[.,]\d{3}\s+-->\s+((\d{1,2}:)?\d{1,2}:\d{2}[.,]\d{3})`)
	vttVoicePattern  = regexp.MustCompile(`<v(?:\.[^ >]*)?\s+([^>]*)>`)
	vttTagPattern    = regexp.MustCompile(`<[^>]*>`)
)

// ConvertVTTToTranscriptJSON converts a WebVTT subtitle file into a
// PodcastIndex JSON transcript file. It is the built-in fallback used when
// the transcript2json helper tool is not available.
func ConvertVTTToTranscriptJSON(vttPath, jsonPath string) error {
	segments, err := parseVTT(vttPath)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return errors.New("no cues found in VTT file")
	}

	doc := TranscriptDoc{Version: "1.0.0", Segments: segments}
	data, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal transcript")
	}

	return os.WriteFile(jsonPath, data, 0o644) //nolint:gosec // served publicly anyway
}

func parseVTT(path string) ([]TranscriptSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open VTT file")
	}
	defer f.Close()

	var (
		segments  []TranscriptSegment
		current   *TranscriptSegment
		inComment bool
		lastBody  string
	)

	flush := func() {
		if current != nil && current.Body != "" {
			// Auto-generated captions often repeat the previous cue's
			// text; drop exact consecutive duplicates.
			if current.Body != lastBody {
				segments = append(segments, *current)
				lastBody = current.Body
			}
		}
		current = nil
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" {
			flush()
			inComment = false
			continue
		}

		if strings.HasPrefix(line, "WEBVTT") || strings.HasPrefix(line, "NOTE") ||
			strings.HasPrefix(line, "STYLE") || strings.HasPrefix(line, "REGION") {
			inComment = true
			continue
		}
		if inComment {
			continue
		}

		if match := vttTimingPattern.FindStringSubmatch(line); match != nil {
			flush()
			timings := strings.SplitN(line, "-->", 2)
			start := parseVTTTimestamp(strings.TrimSpace(timings[0]))
			end := parseVTTTimestamp(strings.TrimSpace(match[2]))
			current = &TranscriptSegment{StartTime: start, EndTime: end}
			continue
		}

		if current == nil {
			// Cue identifier or garbage before the timing line.
			continue
		}

		text := line
		speaker := ""
		if match := vttVoicePattern.FindStringSubmatch(text); match != nil {
			speaker = strings.TrimSpace(match[1])
		}
		text = vttTagPattern.ReplaceAllString(text, "")
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if current.Speaker == "" {
			current.Speaker = speaker
		}
		if current.Body != "" {
			current.Body += " "
		}
		current.Body += text
	}
	flush()

	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "failed to read VTT file")
	}
	return segments, nil
}

// parseVTTTimestamp parses "HH:MM:SS.mmm" or "MM:SS.mmm" into seconds.
func parseVTTTimestamp(value string) float64 {
	value = strings.ReplaceAll(value, ",", ".")
	parts := strings.Split(value, ":")

	var seconds float64
	for _, part := range parts {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0
		}
		seconds = seconds*60 + n
	}
	return seconds
}
