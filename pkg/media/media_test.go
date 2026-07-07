package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	id3v2 "github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbedID3Chapters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episode.mp3")
	// id3v2 prepends a tag to whatever content exists; a real MPEG stream
	// is not required for tagging.
	require.NoError(t, os.WriteFile(path, []byte("FAKE-MPEG-AUDIO-DATA"), 0o600))

	chapters := []Chapter{
		{Start: 0, End: 90 * time.Second, Title: "Intro"},
		{Start: 90 * time.Second, End: 300 * time.Second, Title: "Main — topic"},
	}
	require.NoError(t, EmbedID3Chapters(path, chapters))

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	require.NoError(t, err)
	defer tag.Close()

	chapterFrames := tag.GetFrames("CHAP")
	require.Len(t, chapterFrames, 2)

	first, ok := chapterFrames[0].(id3v2.ChapterFrame)
	require.True(t, ok)
	assert.Equal(t, "chp0", first.ElementID)
	assert.Equal(t, time.Duration(0), first.StartTime)
	assert.Equal(t, 90*time.Second, first.EndTime)
	require.NotNil(t, first.Title)
	assert.Equal(t, "Intro", first.Title.Text)

	second, ok := chapterFrames[1].(id3v2.ChapterFrame)
	require.True(t, ok)
	assert.Equal(t, "Main — topic", second.Title.Text)

	tocFrames := tag.GetFrames("CTOC")
	require.Len(t, tocFrames, 1)

	toc, ok := tocFrames[0].(id3v2.UnknownFrame)
	require.True(t, ok)
	ctoc, err := ctocBody("toc", []string{"chp0", "chp1"})
	require.NoError(t, err)
	assert.Equal(t, ctoc, toc.Body)

	// Audio data must remain intact after the tag.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "FAKE-MPEG-AUDIO-DATA")
}

func TestEmbedID3ChaptersNoChapters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episode.mp3")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	require.NoError(t, EmbedID3Chapters(path, nil))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "data", string(data), "file must be untouched")
}

func TestCTOCBody(t *testing.T) {
	body, err := ctocBody("toc", []string{"chp0", "chp1"})
	require.NoError(t, err)
	expected := []byte("toc\x00\x03\x02chp0\x00chp1\x00")
	assert.Equal(t, expected, body)
}

func TestCTOCBodyRejectsTooManyChildren(t *testing.T) {
	childIDs := make([]string, 256)
	for i := range childIDs {
		childIDs[i] = "chp"
	}
	_, err := ctocBody("toc", childIDs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most 255 chapters")
}

func TestFFMetadata(t *testing.T) {
	chapters := []Chapter{
		{Start: 0, End: 1500 * time.Millisecond, Title: "A=B; #x\\y"},
		{Start: 1500 * time.Millisecond, End: 3 * time.Second, Title: "Plain"},
	}

	out := ffmetadata(chapters)
	assert.Equal(t, ";FFMETADATA1\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1500\ntitle=A\\=B\\; \\#x\\\\y\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=1500\nEND=3000\ntitle=Plain\n", out)
}

// ffmpeg-dependent tests run only when ffmpeg is installed.

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	return path
}

func TestExtractFrame(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	dir := t.TempDir()

	// Generate a 3-second test video.
	videoPath := filepath.Join(dir, "test.mp4")
	out, err := exec.Command(ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=3:size=640x360:rate=10",
		"-pix_fmt", "yuv420p", videoPath).CombinedOutput()
	require.NoError(t, err, string(out))

	framePath := filepath.Join(dir, "frame.jpg")
	require.NoError(t, ExtractFrame(context.Background(), ffmpeg, videoPath, 1.0, 320, framePath))

	info, err := os.Stat(framePath)
	require.NoError(t, err)
	assert.Positive(t, info.Size())
}

func TestEmbedMP4Chapters(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	dir := t.TempDir()

	videoPath := filepath.Join(dir, "test.mp4")
	out, err := exec.Command(ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=3:size=320x180:rate=10",
		"-metadata", "title=Original Title",
		"-pix_fmt", "yuv420p", videoPath).CombinedOutput()
	require.NoError(t, err, string(out))

	chapters := []Chapter{
		{Start: 0, End: 1 * time.Second, Title: "One"},
		{Start: 1 * time.Second, End: 3 * time.Second, Title: "Two"},
	}
	require.NoError(t, EmbedMP4Chapters(context.Background(), ffmpeg, videoPath, chapters))

	// Verify chapters via ffprobe if available, otherwise via ffmpeg -i output.
	if ffprobe, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command(ffprobe, "-v", "error", "-show_chapters", videoPath).CombinedOutput()
		require.NoError(t, err, string(out))
		assert.Contains(t, string(out), "TAG:title=One")
		assert.Contains(t, string(out), "TAG:title=Two")

		out, err = exec.Command(ffprobe, "-v", "error",
			"-show_entries", "format_tags=title",
			"-of", "default=noprint_wrappers=1:nokey=1", videoPath).CombinedOutput()
		require.NoError(t, err, string(out))
		assert.Equal(t, "Original Title\n", string(out))
	}
}
