package rss

import (
	"encoding/xml"
	"strings"

	"github.com/google/uuid"
)

// podcastNamespaceURL is the XML namespace of the Podcasting 2.0 tags.
// See https://podcastindex.org/namespace/1.0
const podcastNamespaceURL = "https://podcastindex.org/namespace/1.0"

// Transcript points to an episode transcript file.
type Transcript struct {
	XMLName  xml.Name `xml:"podcast:transcript"`
	URL      string   `xml:"url,attr"`
	Type     string   `xml:"type,attr"`
	Language string   `xml:"language,attr,omitempty"`
	Rel      string   `xml:"rel,attr,omitempty"`
}

// Transcript media types defined by the podcast namespace specification.
const (
	TranscriptTypeVTT  = "text/vtt"
	TranscriptTypeJSON = "application/json"
	TranscriptTypeSRT  = "application/x-subrip"
)

// Chapters points to a PodcastIndex chapters JSON file for an episode.
type Chapters struct {
	XMLName xml.Name `xml:"podcast:chapters"`
	URL     string   `xml:"url,attr"`
	Type    string   `xml:"type,attr"`
}

// ChaptersTypeJSON is the media type of PodcastIndex JSON chapter files.
const ChaptersTypeJSON = "application/json+chapters"

// Locked tells podcast hosting platforms whether they are allowed to import
// this feed ("yes" forbids importing).
type Locked struct {
	XMLName xml.Name `xml:"podcast:locked"`
	Owner   string   `xml:"owner,attr,omitempty"`
	Value   string   `xml:",chardata"`
}

// Person identifies a person of interest to the podcast or episode.
type Person struct {
	XMLName xml.Name `xml:"podcast:person"`
	Role    string   `xml:"role,attr,omitempty"`
	Group   string   `xml:"group,attr,omitempty"`
	Img     string   `xml:"img,attr,omitempty"`
	Href    string   `xml:"href,attr,omitempty"`
	Name    string   `xml:",chardata"`
}

// SocialInteract points to the root of a comment thread for an episode.
type SocialInteract struct {
	XMLName   xml.Name `xml:"podcast:socialInteract"`
	URI       string   `xml:"uri,attr"`
	Protocol  string   `xml:"protocol,attr"`
	AccountID string   `xml:"accountId,attr,omitempty"`
}

// podcastGUIDNamespace is the UUIDv5 namespace defined by the podcast
// namespace specification for computing podcast:guid values.
var podcastGUIDNamespace = uuid.MustParse("ead4c236-bf58-58c6-a2c6-a6b28d128cb6")

// GUID computes the podcast:guid for a public feed URL as defined by the
// specification: a UUIDv5 of the feed URL with the scheme and trailing
// slashes removed.
func GUID(feedURL string) string {
	url := feedURL
	if i := strings.Index(url, "://"); i != -1 {
		url = url[i+3:]
	}
	url = strings.TrimRight(url, "/")
	return uuid.NewSHA1(podcastGUIDNamespace, []byte(url)).String()
}
