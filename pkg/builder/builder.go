package builder

import (
	"context"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/model"
)

type Builder interface {
	Build(ctx context.Context, cfg *feed.Config) (*model.Feed, error)
}

// APIRecorder records provider API usage for observability. Implementations
// must be safe for concurrent use. A nil APIRecorder is a no-op, so builders
// guard every call.
type APIRecorder interface {
	// AddAPIQuota records estimated API quota units consumed by a provider.
	AddAPIQuota(provider string, units float64)
	// AddAPIRequest records a single provider API request and whether it succeeded.
	AddAPIRequest(provider string, success bool)
}

func New(ctx context.Context, provider model.Provider, key string, downloader Downloader, recorder APIRecorder) (Builder, error) {
	switch provider {
	case model.ProviderYoutube:
		return NewYouTubeBuilder(key, downloader, recorder)
	case model.ProviderVimeo:
		return NewVimeoBuilder(ctx, key)
	case model.ProviderSoundcloud:
		return NewSoundcloudBuilder()
	case model.ProviderTwitch:
		return NewTwitchBuilder(key)
	default:
		return nil, errors.Errorf("unsupported provider %q", provider)
	}
}
