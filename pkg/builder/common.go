package builder

import (
	"time"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

// newFeed creates a model.Feed pre-populated with the fields common to all providers.
func newFeed(cfg *feed.Config, info model.Info) *model.Feed {
	return &model.Feed{
		ItemID:          info.ItemID,
		Provider:        info.Provider,
		LinkType:        info.LinkType,
		Format:          cfg.Format,
		Quality:         cfg.Quality,
		CoverArtQuality: cfg.Custom.CoverArtQuality,
		PageSize:        cfg.PageSize,
		PlaylistSort:    cfg.PlaylistSort,
		PrivateFeed:     cfg.PrivateFeed,
		UpdatedAt:       time.Now().UTC(),
	}
}

// addEpisode appends an episode to the feed and reports whether the feed
// reached its page size.
func addEpisode(f *model.Feed, episode *model.Episode) bool {
	f.Episodes = append(f.Episodes, episode)
	return len(f.Episodes) >= f.PageSize
}
