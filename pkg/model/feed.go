package model

import (
	"time"
)

// Quality to use when downloading episodes
type Quality string

const (
	QualityHigh = Quality("high")
	QualityLow  = Quality("low")
)

// Format to convert episode when downloading episodes
type Format string

const (
	FormatAudio  = Format("audio")
	FormatVideo  = Format("video")
	FormatCustom = Format("custom")
)

// Playlist sorting style
type Sorting string

const (
	SortingDesc = Sorting("desc")
	SortingAsc  = Sorting("asc")
)

type Episode struct {
	// ID of episode
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Thumbnail   string        `json:"thumbnail"`
	Duration    int64         `json:"duration"`
	VideoURL    string        `json:"video_url"`
	PubDate     time.Time     `json:"pub_date"`
	Size        int64         `json:"size"`
	Order       string        `json:"order"`
	Status      EpisodeStatus `json:"status"` // Disk status
	// Enrichment describes transcript/chapter sidecar files stored next to
	// the episode media. Nil for episodes downloaded before enrichment
	// support was added or when enrichment produced nothing.
	Enrichment *EpisodeEnrichment `json:"enrichment,omitempty"`
}

// EpisodeEnrichment records the sidecar artifacts (transcripts, chapters,
// chapter images) generated for a downloaded episode. Values are file names
// within the feed's storage directory, not full paths or URLs.
type EpisodeEnrichment struct {
	TranscriptVTT    string   `json:"transcript_vtt,omitempty"`
	TranscriptJSON   string   `json:"transcript_json,omitempty"`
	TranscriptLang   string   `json:"transcript_lang,omitempty"`
	TranscriptSource string   `json:"transcript_source,omitempty"`
	ChaptersJSON     string   `json:"chapters_json,omitempty"`
	ChaptersSource   string   `json:"chapters_source,omitempty"`
	ChapterImages    []string `json:"chapter_images,omitempty"`
}

// SidecarFiles returns the names of all sidecar files recorded in the
// enrichment, for copying to or deleting from storage.
func (e *EpisodeEnrichment) SidecarFiles() []string {
	if e == nil {
		return nil
	}
	var files []string
	for _, name := range []string{e.TranscriptVTT, e.TranscriptJSON, e.ChaptersJSON} {
		if name != "" {
			files = append(files, name)
		}
	}
	files = append(files, e.ChapterImages...)
	return files
}

type Feed struct {
	ID              string     `json:"feed_id"`
	ItemID          string     `json:"item_id"`
	LinkType        Type       `json:"link_type"` // Either group, channel or user
	Provider        Provider   `json:"provider"`  // Youtube or Vimeo
	PodcastGUID     string     `json:"podcast_guid,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	LastAccess      time.Time  `json:"last_access"`
	ExpirationTime  time.Time  `json:"expiration_time"`
	Format          Format     `json:"format"`
	Quality         Quality    `json:"quality"`
	CoverArtQuality Quality    `json:"cover_art_quality"`
	PageSize        int        `json:"page_size"`
	CoverArt        string     `json:"cover_art"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	PubDate         time.Time  `json:"pub_date"`
	Author          string     `json:"author"`
	ItemURL         string     `json:"item_url"` // Platform specific URL
	Episodes        []*Episode `json:"-"`        // Array of episodes
	UpdatedAt       time.Time  `json:"updated_at"`
	PlaylistSort    Sorting    `json:"playlist_sort"`
	PrivateFeed     bool       `json:"private_feed"`
}

type EpisodeStatus string

const (
	EpisodeNew        = EpisodeStatus("new")        // New episode received via API
	EpisodeDownloaded = EpisodeStatus("downloaded") // Downloaded, encoded and available for download
	EpisodeError      = EpisodeStatus("error")      // Could not download, will retry
	EpisodeCleaned    = EpisodeStatus("cleaned")    // Downloaded and later removed from disk due to update strategy
)
