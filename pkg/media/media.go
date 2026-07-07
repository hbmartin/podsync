// Package media manipulates downloaded media files: embedding chapter
// markers (ID3v2 CHAP/CTOC frames for MP3, container chapters for MP4)
// and extracting chapter frame images with ffmpeg.
package media

import (
	"time"
)

// Chapter is a single chapter marker inside a media file.
type Chapter struct {
	Start time.Duration
	End   time.Duration
	Title string
}
