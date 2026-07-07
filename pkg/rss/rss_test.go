package rss_test

import (
	"strings"
	"testing"
	"time"

	upstream "github.com/eduncan911/podcast"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/rss"
)

// TestUpstreamParity ensures the forked package produces byte-identical
// output to eduncan911/podcast v1.4.2 for the upstream feature set, except
// for the added xmlns:podcast declaration.
func TestUpstreamParity(t *testing.T) {
	pubDate := time.Date(2024, time.March, 1, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2024, time.April, 2, 12, 30, 0, 0, time.UTC)

	old := upstream.New("Test Feed", "http://example.com/feed", "Description here", &pubDate, &updated)
	newer := rss.New("Test Feed", "http://example.com/feed", "Description here", &pubDate, &updated)

	oldItems, newItems := buildUpstream(&old), buildFork(&newer)
	require.Equal(t, oldItems, newItems)

	oldXML := old.String()
	newXML := newer.String()

	unPodcast := strings.Replace(newXML,
		` xmlns:podcast="https://podcastindex.org/namespace/1.0"`, "", 1)
	assert.Equal(t, oldXML, unPodcast)
}

func buildUpstream(p *upstream.Podcast) int {
	p.AddAuthor("John Doe", "john@example.com")
	p.AddAtomLink("http://example.com/feed.xml")
	p.AddCategory("Technology", []string{"Software How-To"})
	p.AddImage("http://example.com/cover.jpg")
	p.AddSubTitle("A sub title")
	p.AddSummary("A summary with <a href=\"http://example.com\">link</a>")
	p.IExplicit = "no"
	p.Language = "en"

	item := upstream.Item{
		GUID:        "video1",
		Title:       "Episode 1",
		Link:        "http://youtube.com/watch?v=1",
		Description: "First episode",
	}
	d := time.Date(2024, time.February, 1, 8, 0, 0, 0, time.UTC)
	item.AddPubDate(&d)
	item.AddSummary("First episode summary")
	item.AddImage("http://example.com/ep1.jpg")
	item.AddDuration(1234)
	item.AddEnclosure("http://example.com/feed1/video1.mp3", upstream.MP3, 4096)
	n, _ := p.AddItem(item)
	return n
}

func buildFork(p *rss.Podcast) int {
	p.AddAuthor("John Doe", "john@example.com")
	p.AddAtomLink("http://example.com/feed.xml")
	p.AddCategory("Technology", []string{"Software How-To"})
	p.AddImage("http://example.com/cover.jpg")
	p.AddSubTitle("A sub title")
	p.AddSummary("A summary with <a href=\"http://example.com\">link</a>")
	p.IExplicit = "no"
	p.Language = "en"

	item := rss.Item{
		GUID:        "video1",
		Title:       "Episode 1",
		Link:        "http://youtube.com/watch?v=1",
		Description: "First episode",
	}
	d := time.Date(2024, time.February, 1, 8, 0, 0, 0, time.UTC)
	item.AddPubDate(&d)
	item.AddSummary("First episode summary")
	item.AddImage("http://example.com/ep1.jpg")
	item.AddDuration(1234)
	item.AddEnclosure("http://example.com/feed1/video1.mp3", rss.MP3, 4096)
	n, _ := p.AddItem(item)
	return n
}

func TestPodcastNamespaceTags(t *testing.T) {
	pubDate := time.Date(2024, time.March, 1, 10, 0, 0, 0, time.UTC)
	p := rss.New("Test", "http://example.com/feed", "Desc", &pubDate, &pubDate)

	p.PodcastGUID = rss.GUID("http://example.com/feed1.xml")
	p.PodcastMedium = "podcast"
	p.PodcastLocked = &rss.Locked{Owner: "owner@example.com", Value: "yes"}
	p.PodcastPersons = []*rss.Person{{Role: "host", Img: "http://example.com/avatar.jpg", Href: "http://youtube.com/@channel", Name: "John Doe"}}

	item := rss.Item{
		Title:       "Episode 1",
		Description: "First episode",
		PodcastTranscripts: []*rss.Transcript{
			{URL: "http://example.com/feed1/video1.vtt", Type: rss.TranscriptTypeVTT, Language: "en"},
			{URL: "http://example.com/feed1/video1.transcript.json", Type: rss.TranscriptTypeJSON, Language: "en"},
		},
		PodcastChapters:        &rss.Chapters{URL: "http://example.com/feed1/video1.chapters.json", Type: rss.ChaptersTypeJSON},
		PodcastSocialInteracts: []*rss.SocialInteract{{URI: "http://youtube.com/watch?v=1", Protocol: "youtube"}},
		IIsClosedCaptioned:     "yes",
	}
	item.AddEnclosure("http://example.com/feed1/video1.mp3", rss.MP3, 4096)
	_, err := p.AddItem(item)
	require.NoError(t, err)

	out := p.String()

	assert.Contains(t, out, `xmlns:podcast="https://podcastindex.org/namespace/1.0"`)
	assert.Contains(t, out, `<podcast:guid>`)
	assert.Contains(t, out, `<podcast:medium>podcast</podcast:medium>`)
	assert.Contains(t, out, `<podcast:locked owner="owner@example.com">yes</podcast:locked>`)
	assert.Contains(t, out, `<podcast:person role="host" img="http://example.com/avatar.jpg" href="http://youtube.com/@channel">John Doe</podcast:person>`)
	assert.Contains(t, out, `<podcast:transcript url="http://example.com/feed1/video1.vtt" type="text/vtt" language="en"></podcast:transcript>`)
	assert.Contains(t, out, `<podcast:transcript url="http://example.com/feed1/video1.transcript.json" type="application/json" language="en"></podcast:transcript>`)
	assert.Contains(t, out, `<podcast:chapters url="http://example.com/feed1/video1.chapters.json" type="application/json+chapters"></podcast:chapters>`)
	assert.Contains(t, out, `<podcast:socialInteract uri="http://youtube.com/watch?v=1" protocol="youtube"></podcast:socialInteract>`)
	assert.Contains(t, out, `<itunes:isClosedCaptioned>yes</itunes:isClosedCaptioned>`)
}

func TestGUID(t *testing.T) {
	// Test vectors from the podcast namespace specification.
	assert.Equal(t, "9b024349-ccf0-5f69-a609-6b82873eab3c", rss.GUID("https://podnews.net/rss"))
	assert.Equal(t, "9b024349-ccf0-5f69-a609-6b82873eab3c", rss.GUID("podnews.net/rss"))
	assert.Equal(t, "9b024349-ccf0-5f69-a609-6b82873eab3c", rss.GUID("https://podnews.net/rss/"))
	assert.Equal(t, "917393e3-1b1e-5cef-ace4-edaa54e1f810", rss.GUID("https://mp3s.nashownotes.com/pc20rss.xml"))
}
