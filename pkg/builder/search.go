package builder

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

// SearchResult describes a channel or playlist discovered via the search endpoint.
type SearchResult struct {
	Provider    model.Provider `json:"provider"`
	Type        model.Type     `json:"type"`
	ID          string         `json:"id"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Thumbnail   string         `json:"thumbnail,omitempty"`
	URL         string         `json:"url"`
}

// YouTubeSearcher resolves YouTube channels, users, handles and playlists to feed metadata,
// and optionally performs keyword search via the YouTube Data API.
type YouTubeSearcher struct {
	client *youtube.Service
	keys   feed.KeyProvider
}

func NewYouTubeSearcher(keys feed.KeyProvider) (*YouTubeSearcher, error) {
	if keys == nil {
		return nil, errors.New("empty YouTube key provider")
	}

	yt, err := youtube.NewService(context.Background(), option.WithHTTPClient(&http.Client{}))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create youtube client")
	}

	return &YouTubeSearcher{client: yt, keys: keys}, nil
}

// Resolve queries metadata for an already parsed channel, user, handle or playlist reference.
// Cost: 3 units for playlists, 5 units for channels/users/handles.
func (s *YouTubeSearcher) Resolve(ctx context.Context, info model.Info) (*SearchResult, error) {
	key := apiKey(s.keys.Get())

	if info.LinkType == model.TypePlaylist {
		resp, err := s.client.Playlists.List([]string{"id", "snippet"}).Id(info.ItemID).Context(ctx).Do(key)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to query playlist: %s", info.ItemID)
		}

		if len(resp.Items) == 0 {
			return nil, model.ErrNotFound
		}

		item := resp.Items[0]
		return &SearchResult{
			Provider:    model.ProviderYoutube,
			Type:        model.TypePlaylist,
			ID:          item.Id,
			Title:       item.Snippet.Title,
			Description: item.Snippet.Description,
			Thumbnail:   bestThumbnail(item.Snippet.Thumbnails),
			URL:         YouTubeCanonicalURL(model.TypePlaylist, item.Id),
		}, nil
	}

	req := s.client.Channels.List([]string{"id", "snippet"})

	switch info.LinkType {
	case model.TypeChannel:
		req = req.Id(info.ItemID)
	case model.TypeUser:
		req = req.ForUsername(info.ItemID)
	case model.TypeHandle:
		req = req.ForHandle(info.ItemID)
	default:
		return nil, errors.Errorf("unsupported link type: %s", info.LinkType)
	}

	resp, err := req.Context(ctx).Do(key)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to query channel: %s", info.ItemID)
	}

	if len(resp.Items) == 0 {
		return nil, model.ErrNotFound
	}

	item := resp.Items[0]
	return &SearchResult{
		Provider:    model.ProviderYoutube,
		Type:        model.TypeChannel,
		ID:          item.Id,
		Title:       item.Snippet.Title,
		Description: item.Snippet.Description,
		Thumbnail:   bestThumbnail(item.Snippet.Thumbnails),
		URL:         YouTubeCanonicalURL(model.TypeChannel, item.Id),
	}, nil
}

// Search performs a keyword search for channels and playlists.
// Cost: 100 units per call, gate behind configuration and cache results.
func (s *YouTubeSearcher) Search(ctx context.Context, query string, limit int64) ([]SearchResult, error) {
	key := apiKey(s.keys.Get())

	resp, err := s.client.Search.List([]string{"snippet"}).
		Q(query).
		Type("channel", "playlist").
		MaxResults(limit).
		Context(ctx).
		Do(key)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to search for: %s", query)
	}

	results := make([]SearchResult, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.Id == nil || item.Snippet == nil {
			continue
		}

		result := SearchResult{
			Provider:    model.ProviderYoutube,
			Title:       item.Snippet.Title,
			Description: item.Snippet.Description,
			Thumbnail:   bestThumbnail(item.Snippet.Thumbnails),
		}

		switch item.Id.Kind {
		case "youtube#channel":
			result.Type = model.TypeChannel
			result.ID = item.Id.ChannelId
		case "youtube#playlist":
			result.Type = model.TypePlaylist
			result.ID = item.Id.PlaylistId
		default:
			continue
		}

		if result.ID == "" {
			continue
		}

		result.URL = YouTubeCanonicalURL(result.Type, result.ID)
		results = append(results, result)
	}

	return results, nil
}

// YouTubeCanonicalURL builds a canonical YouTube URL for the given link type and ID.
func YouTubeCanonicalURL(linkType model.Type, id string) string {
	switch linkType {
	case model.TypePlaylist:
		return fmt.Sprintf("https://youtube.com/playlist?list=%s", id)
	case model.TypeUser:
		return fmt.Sprintf("https://youtube.com/user/%s", id)
	case model.TypeHandle:
		return fmt.Sprintf("https://youtube.com/@%s", id)
	default:
		return fmt.Sprintf("https://youtube.com/channel/%s", id)
	}
}

func bestThumbnail(t *youtube.ThumbnailDetails) string {
	if t == nil {
		return ""
	}

	for _, thumbnail := range []*youtube.Thumbnail{t.Maxres, t.High, t.Medium, t.Default} {
		if thumbnail != nil {
			return thumbnail.Url
		}
	}

	return ""
}
