package builder

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

func TestNewFeed(t *testing.T) {
	cfg := &feed.Config{
		Format:       model.FormatAudio,
		Quality:      model.QualityLow,
		PageSize:     42,
		PlaylistSort: model.SortingDesc,
		PrivateFeed:  true,
		Custom:       feed.Custom{CoverArtQuality: model.QualityHigh},
	}
	info := model.Info{
		ItemID:   "item1",
		Provider: model.ProviderVimeo,
		LinkType: model.TypeChannel,
	}

	f := newFeed(cfg, info)

	require.Equal(t, "item1", f.ItemID)
	require.Equal(t, model.ProviderVimeo, f.Provider)
	require.Equal(t, model.TypeChannel, f.LinkType)
	require.Equal(t, model.FormatAudio, f.Format)
	require.Equal(t, model.QualityLow, f.Quality)
	require.Equal(t, model.QualityHigh, f.CoverArtQuality)
	require.Equal(t, 42, f.PageSize)
	require.Equal(t, model.SortingDesc, f.PlaylistSort)
	require.True(t, f.PrivateFeed)
	require.False(t, f.UpdatedAt.IsZero())
}

func TestAddEpisode(t *testing.T) {
	f := &model.Feed{PageSize: 2}

	require.False(t, addEpisode(f, &model.Episode{ID: "1"}))
	require.True(t, addEpisode(f, &model.Episode{ID: "2"}))
	require.True(t, addEpisode(f, &model.Episode{ID: "3"}))
	require.Len(t, f.Episodes, 3)
}
