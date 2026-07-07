package enrich

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// Chapter is one entry of a PodcastIndex chapters document.
// See https://github.com/Podcastindex-org/podcast-namespace/blob/main/chapters/jsonChapters.md
type Chapter struct {
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime,omitempty"`
	Title     string  `json:"title"`
	Img       string  `json:"img,omitempty"`
	URL       string  `json:"url,omitempty"`
}

// ChaptersDoc is a PodcastIndex chapters JSON document.
type ChaptersDoc struct {
	Version  string    `json:"version"`
	Chapters []Chapter `json:"chapters"`
}

// WriteChaptersJSON writes chapters as a PodcastIndex chapters document.
func WriteChaptersJSON(chapters []Chapter, path string) error {
	doc := ChaptersDoc{Version: "1.2.0", Chapters: chapters}
	data, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal chapters")
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // served publicly anyway
}

// infoJSONChapters extracts creator-defined chapter markers from a yt-dlp
// .info.json file. Returns nil when the video has no chapters.
func infoJSONChapters(infoJSONPath string) ([]Chapter, error) {
	data, err := os.ReadFile(infoJSONPath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read info.json")
	}

	var info struct {
		Chapters []struct {
			StartTime float64 `json:"start_time"`
			EndTime   float64 `json:"end_time"`
			Title     string  `json:"title"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, errors.Wrap(err, "failed to parse info.json")
	}

	chapters := make([]Chapter, 0, len(info.Chapters))
	for _, chapter := range info.Chapters {
		chapters = append(chapters, Chapter{
			StartTime: chapter.StartTime,
			EndTime:   chapter.EndTime,
			Title:     strings.TrimSpace(chapter.Title),
		})
	}
	return chapters, nil
}

// descriptionTimestampPattern matches lines like:
//
//	00:00 Intro
//	1:23:45 - Deep dive
//	[02:30] Q&A
//	• 5:00 — Outro
var descriptionTimestampPattern = regexp.MustCompile(
	`^[\s\p{Zs}]*(?:[-–—•*·▶►\d+.)\]]{0,3}[\s\p{Zs}]*)??[([]?(\d{1,2}(?::\d{1,2}){1,2})[)\]]?[\s\p{Zs}]*[-–—:.|]?[\s\p{Zs}]*(\S.*)$`)

// ParseDescriptionChapters extracts a chapter list from timestamp lines in
// an episode description. It is the built-in fallback used when the
// podcast-chapters helper tool is unavailable. Returns nil unless at least
// two increasing timestamps are found and the first chapter starts at 0:00
// (the convention used by video platforms).
func ParseDescriptionChapters(description string) []Chapter {
	lines := strings.Split(description, "\n")
	chapters := make([]Chapter, 0, len(lines))

	for _, line := range lines {
		match := descriptionTimestampPattern.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}

		start, ok := parseTimestamp(match[1])
		if !ok {
			continue
		}
		title := strings.TrimSpace(match[2])
		if title == "" {
			continue
		}

		chapters = append(chapters, Chapter{StartTime: start, Title: title})
	}

	if len(chapters) < 2 || chapters[0].StartTime != 0 {
		return nil
	}
	for i := 1; i < len(chapters); i++ {
		if chapters[i].StartTime <= chapters[i-1].StartTime {
			return nil
		}
	}
	return chapters
}

// parseTimestamp parses "SS", "MM:SS" or "HH:MM:SS" into seconds.
func parseTimestamp(value string) (float64, bool) {
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}

	var seconds float64
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return 0, false
		}
		seconds = seconds*60 + float64(n)
	}
	return seconds, true
}

// parseFlexibleChapters parses chapter JSON in either the PodcastIndex
// document shape or a bare list, tolerating the key spellings used by
// different chapter tools (start/start_time/startTime, seconds or
// "HH:MM:SS" strings).
func parseFlexibleChapters(data []byte) ([]Chapter, error) {
	var doc struct {
		Chapters []flexibleChapter `json:"chapters"`
	}
	if err := json.Unmarshal(data, &doc); err == nil && len(doc.Chapters) > 0 {
		return convertFlexibleChapters(doc.Chapters), nil
	}

	var list []flexibleChapter
	if err := json.Unmarshal(data, &list); err == nil && len(list) > 0 {
		return convertFlexibleChapters(list), nil
	}

	return nil, errors.New("unrecognized chapters JSON shape")
}

type flexibleChapter struct {
	StartTime  json.RawMessage `json:"startTime"`
	Start      json.RawMessage `json:"start"`
	StartSnake json.RawMessage `json:"start_time"`
	Timestamp  json.RawMessage `json:"timestamp"`
	EndTime    json.RawMessage `json:"endTime"`
	EndSnake   json.RawMessage `json:"end_time"`
	Title      string          `json:"title"`
	Name       string          `json:"name"`
	Summary    string          `json:"summary"`
	Img        string          `json:"img"`
	URL        string          `json:"url"`
}

func convertFlexibleChapters(list []flexibleChapter) []Chapter {
	chapters := make([]Chapter, 0, len(list))
	for _, c := range list {
		start, okStart := firstTimeValue(c.StartTime, c.StartSnake, c.Start, c.Timestamp)
		if !okStart {
			continue
		}
		end, _ := firstTimeValue(c.EndTime, c.EndSnake)

		title := c.Title
		if title == "" {
			title = c.Name
		}
		if title == "" {
			title = c.Summary
		}
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}

		chapters = append(chapters, Chapter{
			StartTime: start,
			EndTime:   end,
			Title:     title,
			Img:       c.Img,
			URL:       c.URL,
		})
	}
	return chapters
}

// firstTimeValue returns the first non-empty raw value parsed as a time:
// either a JSON number of seconds or a "HH:MM:SS"/"MM:SS" string.
func firstTimeValue(values ...json.RawMessage) (float64, bool) {
	for _, raw := range values {
		if len(raw) == 0 {
			continue
		}

		var number float64
		if err := json.Unmarshal(raw, &number); err == nil {
			return number, true
		}

		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if seconds, ok := parseTimestamp(text); ok {
				return seconds, true
			}
			if seconds, err := strconv.ParseFloat(text, 64); err == nil {
				return seconds, true
			}
		}
	}
	return 0, false
}
