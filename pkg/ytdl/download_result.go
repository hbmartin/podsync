package ytdl

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// SubtitleFile is a subtitle sidecar downloaded next to the media file.
type SubtitleFile struct {
	// Lang is the subtitle language tag as reported by yt-dlp (e.g. "en", "en-US").
	Lang string
	// Path is the absolute path of the WebVTT file inside the download directory.
	Path string
}

// DownloadResult describes a completed download: the media file plus any
// sidecar files (metadata, subtitles) that yt-dlp wrote next to it.
//
// The result owns a temporary directory. Callers must consume all files
// before calling Close, which removes the directory and everything in it.
type DownloadResult struct {
	// MediaPath is the absolute path of the downloaded media file.
	MediaPath string
	// InfoJSON is the absolute path of the yt-dlp .info.json file, or "".
	InfoJSON string
	// Subtitles are the downloaded subtitle files, if any were requested and found.
	Subtitles []SubtitleFile

	dir string
}

// Dir returns the temporary working directory owned by this result.
// Additional files may be written to it; they are removed on Close.
func (r *DownloadResult) Dir() string {
	return r.dir
}

// Open opens the media file for reading.
func (r *DownloadResult) Open() (io.ReadCloser, error) {
	return os.Open(r.MediaPath)
}

// Close removes the temporary download directory and all files inside it.
func (r *DownloadResult) Close() error {
	if r.dir == "" {
		return nil
	}
	err := os.RemoveAll(r.dir)
	r.dir = ""
	if err != nil {
		log.Errorf("could not remove temp dir: %v", err)
	}
	return err
}

// discoverSidecars scans the download directory for files produced by
// yt-dlp next to the media file: "<base>.info.json" and "<base>.<lang>.vtt".
func discoverSidecars(dir, baseName string) (infoJSON string, subtitles []SubtitleFile) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.WithError(err).Error("failed to scan download dir for sidecar files")
		return "", nil
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		if name == baseName+".info.json" {
			infoJSON = filepath.Join(dir, name)
			continue
		}

		prefix, suffix := baseName+".", ".vtt"
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) &&
			len(name) > len(prefix)+len(suffix) {
			lang := name[len(prefix) : len(name)-len(suffix)]
			if lang == "" || strings.Contains(lang, ".") {
				continue
			}
			subtitles = append(subtitles, SubtitleFile{
				Lang: lang,
				Path: filepath.Join(dir, name),
			})
		}
	}

	return infoJSON, subtitles
}
