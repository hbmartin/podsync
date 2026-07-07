package enrich

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInfoJSONChapters(t *testing.T) {
	info := `{
		"id": "abc",
		"title": "Video",
		"chapters": [
			{"start_time": 0.0, "end_time": 90.0, "title": "Intro"},
			{"start_time": 90.0, "end_time": 300.0, "title": " Main topic "}
		]
	}`

	path := filepath.Join(t.TempDir(), "v.info.json")
	require.NoError(t, os.WriteFile(path, []byte(info), 0o600))

	chapters, err := infoJSONChapters(path)
	require.NoError(t, err)
	require.Len(t, chapters, 2)
	assert.Equal(t, Chapter{StartTime: 0, EndTime: 90, Title: "Intro"}, chapters[0])
	assert.Equal(t, Chapter{StartTime: 90, EndTime: 300, Title: "Main topic"}, chapters[1])
}

func TestInfoJSONChaptersNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.info.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"id": "abc"}`), 0o600))

	chapters, err := infoJSONChapters(path)
	require.NoError(t, err)
	assert.Empty(t, chapters)
}

func TestParseDescriptionChapters(t *testing.T) {
	description := `Great episode!

Timestamps:
00:00 Intro
02:30 - First topic
[10:45] Second topic
• 1:02:00 — Deep dive
Some random line
1:30:00 Outro

Follow us on twitter.`

	chapters := ParseDescriptionChapters(description)
	require.Len(t, chapters, 5)
	assert.Equal(t, Chapter{StartTime: 0, Title: "Intro"}, chapters[0])
	assert.Equal(t, Chapter{StartTime: 150, Title: "First topic"}, chapters[1])
	assert.Equal(t, Chapter{StartTime: 645, Title: "Second topic"}, chapters[2])
	assert.Equal(t, Chapter{StartTime: 3720, Title: "Deep dive"}, chapters[3])
	assert.Equal(t, Chapter{StartTime: 5400, Title: "Outro"}, chapters[4])
}

func TestParseDescriptionChaptersRejectsNonLists(t *testing.T) {
	// No timestamp at 0:00
	assert.Nil(t, ParseDescriptionChapters("02:30 First topic\n05:00 Second"))
	// Single timestamp only
	assert.Nil(t, ParseDescriptionChapters("00:00 Intro"))
	// Non-increasing timestamps
	assert.Nil(t, ParseDescriptionChapters("00:00 Intro\n05:00 A\n03:00 B"))
	// No timestamps at all
	assert.Nil(t, ParseDescriptionChapters("Just a normal description."))
}

func TestParseFlexibleChapters(t *testing.T) {
	// PodcastIndex document shape
	doc := `{"version": "1.2.0", "chapters": [
		{"startTime": 0, "title": "Intro"},
		{"startTime": 120.5, "endTime": 300, "title": "Topic", "img": "http://x/1.jpg"}
	]}`
	chapters, err := parseFlexibleChapters([]byte(doc))
	require.NoError(t, err)
	require.Len(t, chapters, 2)
	assert.Equal(t, 120.5, chapters[1].StartTime)
	assert.Equal(t, 300.0, chapters[1].EndTime)
	assert.Equal(t, "http://x/1.jpg", chapters[1].Img)

	// Bare list with snake_case keys and string timestamps
	list := `[
		{"start_time": "00:00", "title": "Intro"},
		{"timestamp": "1:02:03", "name": "Later"}
	]`
	chapters, err = parseFlexibleChapters([]byte(list))
	require.NoError(t, err)
	require.Len(t, chapters, 2)
	assert.Equal(t, 0.0, chapters[0].StartTime)
	assert.Equal(t, "Intro", chapters[0].Title)
	assert.Equal(t, 3723.0, chapters[1].StartTime)
	assert.Equal(t, "Later", chapters[1].Title)

	_, err = parseFlexibleChapters([]byte(`{"foo": "bar"}`))
	assert.Error(t, err)
}

func TestWriteChaptersJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.chapters.json")
	chapters := []Chapter{
		{StartTime: 0, EndTime: 90, Title: "Intro", Img: "http://x/1.jpg"},
		{StartTime: 90, Title: "Rest"},
	}
	require.NoError(t, WriteChaptersJSON(chapters, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc ChaptersDoc
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, "1.2.0", doc.Version)
	assert.Equal(t, chapters, doc.Chapters)
}

func TestFillEndTimes(t *testing.T) {
	chapters := []Chapter{
		{StartTime: 0, Title: "A"},
		{StartTime: 100, Title: "B"},
		{StartTime: 200, Title: "C"},
	}
	fillEndTimes(chapters, 500)
	assert.Equal(t, 100.0, chapters[0].EndTime)
	assert.Equal(t, 200.0, chapters[1].EndTime)
	assert.Equal(t, 500.0, chapters[2].EndTime)

	// Unknown total duration leaves the last end time empty
	chapters = []Chapter{{StartTime: 0, Title: "A"}, {StartTime: 10, Title: "B"}}
	fillEndTimes(chapters, 0)
	assert.Equal(t, 10.0, chapters[0].EndTime)
	assert.Equal(t, 0.0, chapters[1].EndTime)
}

func TestChapterImageName(t *testing.T) {
	assert.Equal(t, "ep.chapter-01.jpg", ChapterImageName("ep", 0, 5))
	assert.Equal(t, "ep.chapter-10.jpg", ChapterImageName("ep", 9, 10))
	assert.Equal(t, "ep.chapter-100.jpg", ChapterImageName("ep", 99, 101))
}

func TestSidecarNames(t *testing.T) {
	assert.Equal(t, "ep.vtt", TranscriptVTTName("ep"))
	assert.Equal(t, "ep.transcript.json", TranscriptJSONName("ep"))
	assert.Equal(t, "ep.chapters.json", ChaptersJSONName("ep"))
}
