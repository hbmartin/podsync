package media

import (
	"bytes"
	"fmt"

	id3v2 "github.com/bogem/id3v2/v2"
	"github.com/pkg/errors"
)

// EmbedID3Chapters writes ID3v2 CHAP frames plus a CTOC table of contents
// into the MP3 file at path, so chapters are available to players even for
// downloaded/offline files. Existing chapter frames are replaced.
func EmbedID3Chapters(path string, chapters []Chapter) error {
	if len(chapters) == 0 {
		return nil
	}

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return errors.Wrap(err, "failed to open mp3 for tagging")
	}
	defer tag.Close()

	tag.DeleteFrames("CHAP")
	tag.DeleteFrames("CTOC")

	elementIDs := make([]string, 0, len(chapters))
	for i, chapter := range chapters {
		elementID := fmt.Sprintf("chp%d", i)
		elementIDs = append(elementIDs, elementID)

		tag.AddChapterFrame(id3v2.ChapterFrame{
			ElementID:   elementID,
			StartTime:   chapter.Start,
			EndTime:     chapter.End,
			StartOffset: id3v2.IgnoredOffset,
			EndOffset:   id3v2.IgnoredOffset,
			Title: &id3v2.TextFrame{
				Encoding: id3v2.EncodingUTF8,
				Text:     chapter.Title,
			},
		})
	}

	tag.AddFrame("CTOC", id3v2.UnknownFrame{Body: ctocBody("toc", elementIDs)})

	if err := tag.Save(); err != nil {
		return errors.Wrap(err, "failed to save id3 chapter frames")
	}
	return nil
}

// ctocBody encodes an ID3v2 CTOC frame body: a top-level, ordered table of
// contents listing the CHAP element IDs.
// Layout per id3v2-chapters-1.0: elementID NUL flags entryCount childID NUL...
func ctocBody(elementID string, childIDs []string) []byte {
	var buf bytes.Buffer
	buf.WriteString(elementID)
	buf.WriteByte(0)
	buf.WriteByte(0x03) // flags: top-level | ordered
	buf.WriteByte(byte(len(childIDs)))
	for _, id := range childIDs {
		buf.WriteString(id)
		buf.WriteByte(0)
	}
	return buf.Bytes()
}
