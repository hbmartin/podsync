package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// ExtractFrame saves a single video frame at the given position as a JPEG,
// scaled down to at most maxWidth pixels wide (never upscaled).
func ExtractFrame(ctx context.Context, ffmpeg, videoPath string, at float64, maxWidth int, outPath string) error {
	args := []string{
		"-y",
		"-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", at),
		"-i", videoPath,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", maxWidth),
		"-q:v", "3",
		outPath,
	}

	output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "ffmpeg frame extraction failed: %s", string(output))
	}

	if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
		return errors.New("ffmpeg did not produce a frame image")
	}
	return nil
}

// EmbedMP4Chapters remuxes the MP4 file in place, adding the chapters as
// container chapter markers. Streams are copied, not re-encoded.
func EmbedMP4Chapters(ctx context.Context, ffmpeg, mp4Path string, chapters []Chapter) error {
	if len(chapters) == 0 {
		return nil
	}

	dir := filepath.Dir(mp4Path)

	metaFile, err := os.CreateTemp(dir, "chapters-*.txt")
	if err != nil {
		return errors.Wrap(err, "failed to create ffmetadata file")
	}
	defer os.Remove(metaFile.Name())

	if _, err := metaFile.WriteString(ffmetadata(chapters)); err != nil {
		metaFile.Close()
		return errors.Wrap(err, "failed to write ffmetadata file")
	}
	if err := metaFile.Close(); err != nil {
		return errors.Wrap(err, "failed to close ffmetadata file")
	}

	outPath := mp4Path + ".chapters" + filepath.Ext(mp4Path)
	args := []string{
		"-y",
		"-loglevel", "error",
		"-i", mp4Path,
		"-i", metaFile.Name(),
		"-map", "0",
		"-map_metadata", "0",
		"-map_chapters", "1",
		"-codec", "copy",
		outPath,
	}

	output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
	if err != nil {
		os.Remove(outPath)
		return errors.Wrapf(err, "ffmpeg chapter remux failed: %s", string(output))
	}

	if err := os.Rename(outPath, mp4Path); err != nil {
		os.Remove(outPath)
		return errors.Wrap(err, "failed to replace mp4 with remuxed file")
	}
	return nil
}

// ffmetadata renders chapters in ffmpeg's FFMETADATA1 format.
func ffmetadata(chapters []Chapter) string {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	for _, chapter := range chapters {
		b.WriteString("[CHAPTER]\nTIMEBASE=1/1000\n")
		fmt.Fprintf(&b, "START=%d\n", chapter.Start.Milliseconds())
		fmt.Fprintf(&b, "END=%d\n", chapter.End.Milliseconds())
		fmt.Fprintf(&b, "title=%s\n", escapeFFMetadata(chapter.Title))
	}
	return b.String()
}

// escapeFFMetadata escapes the characters that are special in ffmetadata
// values: '=', ';', '#', '\' and newline.
func escapeFFMetadata(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '=', ';', '#', '\\', '\n':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
