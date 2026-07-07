package feed

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/model"
	itunes "github.com/mxpv/podsync/pkg/rss"
)

// sort.Interface implementation
type timeSlice []*model.Episode

const defaultFilenameTemplate = "{{id}}"

var (
	filenameTemplateTokenPattern       = regexp.MustCompile(`{{\s*([a-z_]+)\s*}}`)
	filenameTemplatePlaceholderPattern = regexp.MustCompile(`{{\s*([^{}]*)\s*}}`)
	filenameTemplateTokenNamePattern   = regexp.MustCompile(`^[a-z_]+$`)
	validExtensionPattern              = regexp.MustCompile(`^[a-z0-9]+$`)
	invalidFilenameCharsPattern        = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)
	multiWhitespacePattern             = regexp.MustCompile(`\s+`)
)

var filenameTemplateAllowedTokens = map[string]struct{}{
	"id":       {},
	"title":    {},
	"pub_date": {},
	"feed_id":  {},
}

func (p timeSlice) Len() int {
	return len(p)
}

// In descending order
func (p timeSlice) Less(i, j int) bool {
	return p[i].PubDate.After(p[j].PubDate)
}

func (p timeSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func Build(_ctx context.Context, feed *model.Feed, cfg *Config, hostname string) (*itunes.Podcast, error) {
	const (
		podsyncGenerator = "Podsync generator (support us at https://github.com/mxpv/podsync)"
		defaultCategory  = "TV & Film"
	)

	var (
		now         = time.Now().UTC()
		author      = feed.Author
		title       = feed.Title
		description = feed.Description
		feedLink    = feed.ItemURL
	)

	if author == "<notfound>" {
		author = feed.Title
	}

	if cfg.Custom.Author != "" {
		author = cfg.Custom.Author
	}

	if cfg.Custom.Title != "" {
		title = cfg.Custom.Title
	}

	if cfg.Custom.Description != "" {
		description = cfg.Custom.Description
	}

	if cfg.Custom.Link != "" {
		feedLink = cfg.Custom.Link
	}

	p := itunes.New(title, feedLink, description, &feed.PubDate, &now)
	p.Generator = podsyncGenerator
	p.AddSubTitle(title)
	p.IAuthor = author
	p.AddSummary(description)

	if feed.PrivateFeed {
		p.IBlock = "yes"
	}

	if cfg.Custom.OwnerName != "" && cfg.Custom.OwnerEmail != "" {
		p.IOwner = &itunes.Author{
			Name:  cfg.Custom.OwnerName,
			Email: cfg.Custom.OwnerEmail,
		}
	}

	if cfg.Custom.CoverArt != "" {
		p.AddImage(cfg.Custom.CoverArt)
	} else {
		p.AddImage(feed.CoverArt)
	}

	if cfg.Custom.Category != "" {
		p.AddCategory(cfg.Custom.Category, cfg.Custom.Subcategories)
	} else {
		p.AddCategory(defaultCategory, cfg.Custom.Subcategories)
	}

	if cfg.Custom.Explicit {
		p.IExplicit = "true"
	} else {
		p.IExplicit = "false"
	}

	if cfg.Custom.Language != "" {
		p.Language = cfg.Custom.Language
	}

	// Podcasting 2.0 channel tags (https://podcastindex.org/namespace/1.0)
	feedURL := fmt.Sprintf("%s/%s.xml", strings.TrimRight(hostname, "/"), cfg.ID)
	p.PodcastGUID = itunes.GUID(feedURL)

	if feed.Format == model.FormatVideo {
		p.PodcastMedium = "video"
	} else {
		p.PodcastMedium = "podcast"
	}

	lockedValue := ""
	switch {
	case cfg.Custom.Locked != nil && *cfg.Custom.Locked:
		lockedValue = "yes"
	case cfg.Custom.Locked != nil:
		lockedValue = "no"
	case cfg.Custom.OwnerEmail != "":
		// Podsync feeds are personal; mark them as not importable by
		// hosting platforms whenever there is an owner contact to verify
		// unlock requests against.
		lockedValue = "yes"
	}
	if lockedValue != "" {
		p.PodcastLocked = &itunes.Locked{Owner: cfg.Custom.OwnerEmail, Value: lockedValue}
	}

	if author != "" {
		personImg := cfg.Custom.CoverArt
		if personImg == "" {
			personImg = feed.CoverArt
		}
		p.PodcastPersons = append(p.PodcastPersons, &itunes.Person{
			Role: "host",
			Img:  personImg,
			Href: feed.ItemURL,
			Name: author,
		})
	}

	for _, episode := range feed.Episodes {
		if episode.PubDate.IsZero() {
			episode.PubDate = now
		}
	}

	// Sort all episodes in descending order
	sort.Sort(timeSlice(feed.Episodes))

	for i, episode := range feed.Episodes {
		if episode.Status != model.EpisodeDownloaded {
			// Skip episodes that are not yet downloaded or have been removed
			continue
		}

		item := itunes.Item{
			GUID:        episode.ID,
			Link:        episode.VideoURL,
			Title:       episode.Title,
			Description: episode.Description,
			ISubtitle:   episode.Title,
			// Some app prefer 1-based order
			IOrder: strconv.Itoa(i + 1),
		}

		item.AddPubDate(&episode.PubDate)
		item.AddSummary(episode.Description)
		item.AddImage(episode.Thumbnail)
		item.AddDuration(episode.Duration)

		enclosureType := itunes.MP4
		if feed.Format == model.FormatAudio {
			enclosureType = itunes.MP3
		}
		if feed.Format == model.FormatCustom {
			enclosureType = EnclosureFromExtension(cfg)
		}

		var (
			episodeName = EpisodeName(cfg, episode)
			feedBaseURL = fmt.Sprintf("%s/%s", strings.TrimRight(hostname, "/"), cfg.ID)
			downloadURL = fmt.Sprintf("%s/%s", feedBaseURL, episodeName)
		)

		item.AddEnclosure(downloadURL, enclosureType, episode.Size)

		if enrichment := episode.Enrichment; enrichment != nil {
			if enrichment.TranscriptVTT != "" {
				item.PodcastTranscripts = append(item.PodcastTranscripts, &itunes.Transcript{
					URL:      fmt.Sprintf("%s/%s", feedBaseURL, enrichment.TranscriptVTT),
					Type:     itunes.TranscriptTypeVTT,
					Language: enrichment.TranscriptLang,
				})
			}
			if enrichment.TranscriptJSON != "" {
				item.PodcastTranscripts = append(item.PodcastTranscripts, &itunes.Transcript{
					URL:      fmt.Sprintf("%s/%s", feedBaseURL, enrichment.TranscriptJSON),
					Type:     itunes.TranscriptTypeJSON,
					Language: enrichment.TranscriptLang,
				})
			}
			if len(item.PodcastTranscripts) > 0 {
				item.IIsClosedCaptioned = "yes"
			}
			if enrichment.ChaptersJSON != "" {
				item.PodcastChapters = &itunes.Chapters{
					URL:  fmt.Sprintf("%s/%s", feedBaseURL, enrichment.ChaptersJSON),
					Type: itunes.ChaptersTypeJSON,
				}
			}
		}

		if episode.VideoURL != "" {
			item.PodcastSocialInteracts = []*itunes.SocialInteract{{
				URI: episode.VideoURL,
				// The spec's protocol slug list has no entry for video
				// platforms; use the provider name so apps that only look
				// at the URI still work.
				Protocol: strings.ToLower(string(feed.Provider)),
			}}
		}

		// p.AddItem requires description to be not empty, use workaround
		if item.Description == "" {
			item.Description = " "
		}

		if cfg.Custom.Explicit {
			item.IExplicit = "true"
		} else {
			item.IExplicit = "false"
		}

		if _, err := p.AddItem(item); err != nil {
			return nil, errors.Wrapf(err, "failed to add item to podcast (id %q)", episode.ID)
		}
	}

	return &p, nil
}

func EpisodeName(feedConfig *Config, episode *model.Episode) string {
	return fmt.Sprintf("%s.%s", EpisodeBaseName(feedConfig, episode), episodeExtension(feedConfig))
}

func LegacyEpisodeName(feedConfig *Config, episode *model.Episode) string {
	return fmt.Sprintf("%s.%s", episode.ID, episodeExtension(feedConfig))
}

func EnclosureFromExtension(feedConfig *Config) itunes.EnclosureType {
	ext := normalizeExtension(feedConfig.CustomFormat.Extension)

	switch ext {
	case "m4a":
		return itunes.M4A
	case "m4v":
		return itunes.M4V
	case "mp4":
		return itunes.MP4
	case "mp3":
		return itunes.MP3
	case "mov":
		return itunes.MOV
	case "pdf":
		return itunes.PDF
	case "epub":
		return itunes.EPUB
	default:
		return -1
	}
}

func EpisodeBaseName(feedConfig *Config, episode *model.Episode) string {
	template := strings.TrimSpace(feedConfig.FilenameTemplate)
	if template == "" {
		template = defaultFilenameTemplate
	}

	pubDate := "0000-00-00"
	if !episode.PubDate.IsZero() {
		pubDate = episode.PubDate.UTC().Format("2006-01-02")
	}

	replacements := map[string]string{
		"id":       episode.ID,
		"title":    episode.Title,
		"pub_date": pubDate,
		"feed_id":  feedConfig.ID,
	}

	rendered := filenameTemplateTokenPattern.ReplaceAllStringFunc(template, func(token string) string {
		match := filenameTemplateTokenPattern.FindStringSubmatch(token)
		if len(match) < 2 {
			return ""
		}
		return replacements[match[1]]
	})

	name := sanitizeFilename(rendered)
	if name == "" {
		name = sanitizeFilename(episode.ID)
	}
	if name == "" {
		name = "episode"
	}
	return name
}

func ValidateFilenameTemplate(template string) error {
	template = strings.TrimSpace(template)
	if template == "" {
		return nil
	}

	matches := filenameTemplatePlaceholderPattern.FindAllStringSubmatch(template, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		token := strings.TrimSpace(match[1])
		if !filenameTemplateTokenNamePattern.MatchString(token) {
			return errors.Errorf("unknown filename template token %q", token)
		}
		if _, ok := filenameTemplateAllowedTokens[token]; !ok {
			return errors.Errorf("unknown filename template token %q", token)
		}
	}

	return nil
}

func ValidateCustomExtension(extension string) error {
	normalized := normalizeExtension(extension)
	if normalized == "" {
		return errors.New("custom format extension cannot be empty")
	}
	if !validExtensionPattern.MatchString(normalized) {
		return errors.Errorf("custom format extension %q must contain only letters and numbers", extension)
	}
	return nil
}

func episodeExtension(feedConfig *Config) string {
	defaultExt := "mp4"
	if feedConfig.Format == model.FormatAudio {
		defaultExt = "mp3"
	}

	ext := defaultExt
	if feedConfig.Format == model.FormatCustom {
		ext = normalizeExtension(feedConfig.CustomFormat.Extension)
	}
	if ext == "" || !validExtensionPattern.MatchString(ext) {
		ext = defaultExt
	}
	return ext
}

func normalizeExtension(extension string) string {
	normalized := strings.TrimSpace(extension)
	normalized = strings.TrimPrefix(normalized, ".")
	return strings.ToLower(normalized)
}

func sanitizeFilename(value string) string {
	cleaned := strings.TrimSpace(value)
	cleaned = invalidFilenameCharsPattern.ReplaceAllString(cleaned, "")
	cleaned = multiWhitespacePattern.ReplaceAllString(cleaned, "_")
	cleaned = strings.Trim(cleaned, "._- ")
	return cleaned
}
