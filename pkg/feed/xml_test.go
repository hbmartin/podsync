package feed

import (
	"context"
	"testing"
	"time"

	"github.com/mxpv/podsync/pkg/model"
	itunes "github.com/mxpv/podsync/pkg/rss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildXML(t *testing.T) {
	feed := model.Feed{
		Episodes: []*model.Episode{
			{
				ID:          "1",
				Status:      model.EpisodeDownloaded,
				Title:       "title",
				Description: "description",
			},
		},
	}

	cfg := Config{
		ID:     "test",
		Custom: Custom{Description: "description", Category: "Technology", Subcategories: []string{"Gadgets", "Podcasting"}},
	}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	assert.NoError(t, err)

	assert.EqualValues(t, "description", out.Description)
	assert.EqualValues(t, "Technology", out.Category)

	require.Len(t, out.ICategories, 1)
	category := out.ICategories[0]
	assert.EqualValues(t, "Technology", category.Text)

	require.Len(t, category.ICategories, 2)
	assert.EqualValues(t, "Gadgets", category.ICategories[0].Text)
	assert.EqualValues(t, "Podcasting", category.ICategories[1].Text)

	require.Len(t, out.Items, 1)
	require.NotNil(t, out.Items[0].Enclosure)
	assert.EqualValues(t, out.Items[0].Enclosure.URL, "http://localhost/test/1.mp4")
	assert.EqualValues(t, out.Items[0].Enclosure.Type, itunes.MP4)
}

func TestBuildXMLWithFilenameTemplate(t *testing.T) {
	feed := model.Feed{
		Episodes: []*model.Episode{
			{
				ID:          "video123",
				Status:      model.EpisodeDownloaded,
				Title:       "A title / with chars",
				Description: "description",
				PubDate:     time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	cfg := Config{
		ID:               "test",
		Format:           model.FormatVideo,
		FilenameTemplate: "{{pub_date}}_{{title}}_{{id}}",
	}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	require.NoError(t, err)
	require.Len(t, out.Items, 1)
	require.NotNil(t, out.Items[0].Enclosure)
	assert.Equal(t, "video123", out.Items[0].GUID)
	assert.Equal(t, "http://localhost/test/2025-12-31_A_title_with_chars_video123.mp4", out.Items[0].Enclosure.URL)
}

func TestEpisodeNameTemplate(t *testing.T) {
	cfg := &Config{
		ID:               "test",
		Format:           model.FormatVideo,
		FilenameTemplate: "{{pub_date}}_{{title}}_{{id}}",
	}

	episode := &model.Episode{
		ID:      "abc123",
		Title:   "My / Video: Title?",
		PubDate: time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC),
	}

	assert.Equal(t, "2026-02-08_My_Video_Title_abc123.mp4", EpisodeName(cfg, episode))
}

func TestValidateFilenameTemplate(t *testing.T) {
	assert.NoError(t, ValidateFilenameTemplate(""))
	assert.NoError(t, ValidateFilenameTemplate("{{pub_date}}_{{title}}_{{id}}"))
	assert.Error(t, ValidateFilenameTemplate("{{unknown}}_{{id}}"))
	assert.Error(t, ValidateFilenameTemplate("{{ID}}_{{id}}"))
	assert.Error(t, ValidateFilenameTemplate("{{pub-date}}_{{id}}"))
}

func TestValidateCustomExtension(t *testing.T) {
	assert.NoError(t, ValidateCustomExtension(".M4A"))
	assert.Error(t, ValidateCustomExtension(""))
	assert.Error(t, ValidateCustomExtension("../mp3"))
}

func TestEpisodeNameWithCustomExtensionNormalization(t *testing.T) {
	cfg := &Config{
		ID:     "test",
		Format: model.FormatCustom,
		CustomFormat: CustomFormat{
			Extension: ".M4A",
		},
	}
	episode := &model.Episode{ID: "abc123", Title: "Title"}
	assert.Equal(t, "abc123.m4a", EpisodeName(cfg, episode))

	cfg.CustomFormat.Extension = "../bad"
	assert.Equal(t, "abc123.mp4", EpisodeName(cfg, episode))
}

func TestBuildXMLPodcastNamespaceChannelTags(t *testing.T) {
	feed := model.Feed{
		Format:   model.FormatAudio,
		Provider: model.ProviderYoutube,
		Author:   "Channel Author",
		CoverArt: "http://example.com/avatar.jpg",
		ItemURL:  "https://youtube.com/channel/123",
		Episodes: []*model.Episode{
			{
				ID:          "1",
				Status:      model.EpisodeDownloaded,
				Title:       "title",
				Description: "description",
				VideoURL:    "https://youtube.com/watch?v=1",
			},
		},
	}

	cfg := Config{
		ID:     "test",
		Format: model.FormatAudio,
		Custom: Custom{OwnerName: "Owner", OwnerEmail: "owner@example.com"},
	}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	require.NoError(t, err)

	assert.Equal(t, itunes.GUID("http://localhost/test.xml"), out.PodcastGUID)
	assert.Equal(t, "podcast", out.PodcastMedium)

	require.NotNil(t, out.PodcastLocked)
	assert.Equal(t, "yes", out.PodcastLocked.Value)
	assert.Equal(t, "owner@example.com", out.PodcastLocked.Owner)

	require.Len(t, out.PodcastPersons, 1)
	assert.Equal(t, "Channel Author", out.PodcastPersons[0].Name)
	assert.Equal(t, "host", out.PodcastPersons[0].Role)
	assert.Equal(t, "http://example.com/avatar.jpg", out.PodcastPersons[0].Img)
	assert.Equal(t, "https://youtube.com/channel/123", out.PodcastPersons[0].Href)

	require.Len(t, out.Items, 1)
	require.Len(t, out.Items[0].PodcastSocialInteracts, 1)
	assert.Equal(t, "https://youtube.com/watch?v=1", out.Items[0].PodcastSocialInteracts[0].URI)
	assert.Equal(t, "youtube", out.Items[0].PodcastSocialInteracts[0].Protocol)
}

func TestBuildXMLPodcastMediumVideo(t *testing.T) {
	feed := model.Feed{Format: model.FormatVideo}
	cfg := Config{ID: "test", Format: model.FormatVideo}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	require.NoError(t, err)
	assert.Equal(t, "video", out.PodcastMedium)
	assert.Nil(t, out.PodcastLocked)
}

func TestBuildXMLLockedOverrides(t *testing.T) {
	feed := model.Feed{}
	no := false
	cfg := Config{ID: "test", Custom: Custom{OwnerEmail: "owner@example.com", Locked: &no}}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	require.NoError(t, err)
	require.NotNil(t, out.PodcastLocked)
	assert.Equal(t, "no", out.PodcastLocked.Value)

	yes := true
	cfg = Config{ID: "test", Custom: Custom{Locked: &yes}}
	out, err = Build(context.Background(), &feed, &cfg, "http://localhost/")
	require.NoError(t, err)
	require.NotNil(t, out.PodcastLocked)
	assert.Equal(t, "yes", out.PodcastLocked.Value)
	assert.Equal(t, "", out.PodcastLocked.Owner)
}

func TestBuildXMLEnrichmentTags(t *testing.T) {
	feed := model.Feed{
		Format: model.FormatAudio,
		Episodes: []*model.Episode{
			{
				ID:          "video1",
				Status:      model.EpisodeDownloaded,
				Title:       "title",
				Description: "description",
				Enrichment: &model.EpisodeEnrichment{
					TranscriptVTT:  "video1.vtt",
					TranscriptJSON: "video1.transcript.json",
					TranscriptLang: "en",
					ChaptersJSON:   "video1.chapters.json",
				},
			},
			{
				ID:          "video2",
				Status:      model.EpisodeDownloaded,
				Title:       "title 2",
				Description: "description 2",
			},
		},
	}

	cfg := Config{ID: "test", Format: model.FormatAudio}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	require.NoError(t, err)
	require.Len(t, out.Items, 2)

	enriched := out.Items[0]
	require.Len(t, enriched.PodcastTranscripts, 2)
	assert.Equal(t, "http://localhost/test/video1.vtt", enriched.PodcastTranscripts[0].URL)
	assert.Equal(t, itunes.TranscriptTypeVTT, enriched.PodcastTranscripts[0].Type)
	assert.Equal(t, "en", enriched.PodcastTranscripts[0].Language)
	assert.Equal(t, "http://localhost/test/video1.transcript.json", enriched.PodcastTranscripts[1].URL)
	assert.Equal(t, itunes.TranscriptTypeJSON, enriched.PodcastTranscripts[1].Type)
	assert.Equal(t, "yes", enriched.IIsClosedCaptioned)
	require.NotNil(t, enriched.PodcastChapters)
	assert.Equal(t, "http://localhost/test/video1.chapters.json", enriched.PodcastChapters.URL)
	assert.Equal(t, itunes.ChaptersTypeJSON, enriched.PodcastChapters.Type)

	bare := out.Items[1]
	assert.Empty(t, bare.PodcastTranscripts)
	assert.Nil(t, bare.PodcastChapters)
	assert.Empty(t, bare.IIsClosedCaptioned)
}
