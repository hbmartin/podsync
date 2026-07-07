package builder

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"

	"github.com/mxpv/podsync/pkg/model"
)

// MockTransport implements http.RoundTripper for testing. It matches requests
// by URL path, records them, and replies with canned JSON bodies.
type MockTransport struct {
	responses map[string]string // URL path → JSON body
	requests  []*http.Request
}

func (m *MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.requests = append(m.requests, req)

	if body, exists := m.responses[req.URL.Path]; exists {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json; charset=UTF-8"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	return &http.Response{
		StatusCode: 404,
		Body:       http.NoBody,
	}, nil
}

func newTestYouTubeBuilder(t *testing.T, transport *MockTransport) *YouTubeBuilder {
	t.Helper()

	client := &http.Client{Transport: transport}
	yt, err := youtube.NewService(context.Background(), option.WithHTTPClient(client))
	require.NoError(t, err)

	return &YouTubeBuilder{
		client:  yt,
		key:     apiKey("test-api-key"),
		handles: &handleCache{m: map[string]string{}},
	}
}

func TestResolveHandle(t *testing.T) {
	const channelsPath = "/youtube/v3/channels"

	tests := []struct {
		name       string
		handle     string
		mockResp   string
		expected   string
		wantErrMsg string
	}{
		{
			name:     "valid handle",
			handle:   "testhandle",
			mockResp: `{"items": [{"id": "UC_test_channel_id_123"}]}`,
			expected: "UC_test_channel_id_123",
		},
		{
			name:       "handle not found",
			handle:     "nonexistent",
			mockResp:   `{"items": []}`,
			wantErrMsg: model.ErrNotFound.Error(),
		},
		{
			name:       "empty channel ID",
			handle:     "badhandle",
			mockResp:   `{"items": [{"id": ""}]}`,
			wantErrMsg: "channel ID not found for handle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &MockTransport{responses: map[string]string{channelsPath: tt.mockResp}}
			yt := newTestYouTubeBuilder(t, transport)

			channelID, err := yt.resolveHandle(context.Background(), tt.handle)

			if tt.wantErrMsg != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected, channelID)

			// The lookup must use the cheap channels.list forHandle call (~1 unit),
			// not the 100-unit search.list call.
			require.Len(t, transport.requests, 1)
			query := transport.requests[0].URL.Query()
			require.Equal(t, tt.handle, query.Get("forHandle"))
			require.Equal(t, "id", query.Get("part"))
		})
	}
}

func TestResolveHandleCached(t *testing.T) {
	const channelsPath = "/youtube/v3/channels"

	transport := &MockTransport{
		responses: map[string]string{channelsPath: `{"items": [{"id": "UC_cached_channel"}]}`},
	}
	yt := newTestYouTubeBuilder(t, transport)

	first, err := yt.resolveHandle(context.Background(), "somehandle")
	require.NoError(t, err)
	require.Equal(t, "UC_cached_channel", first)

	// Second resolution must be served from the cache, including
	// case-insensitive and @-prefixed variants of the same handle.
	for _, handle := range []string{"somehandle", "SomeHandle", "@somehandle"} {
		id, err := yt.resolveHandle(context.Background(), handle)
		require.NoError(t, err)
		require.Equal(t, "UC_cached_channel", id)
	}

	require.Len(t, transport.requests, 1, "cached lookups must not hit the API")
}

func TestParseURLWithHandles(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected model.Info
		wantErr  bool
	}{
		{
			name: "valid handle URL",
			url:  "https://www.youtube.com/@testhandle",
			expected: model.Info{
				LinkType: model.TypeHandle,
				Provider: model.ProviderYoutube,
				ItemID:   "testhandle",
			},
			wantErr: false,
		},
		{
			name: "handle URL with videos path",
			url:  "https://youtube.com/@mychannel/videos",
			expected: model.Info{
				LinkType: model.TypeHandle,
				Provider: model.ProviderYoutube,
				ItemID:   "mychannel",
			},
			wantErr: false,
		},
		{
			name:    "invalid handle URL",
			url:     "https://www.youtube.com/@",
			wantErr: true,
		},
		{
			name: "regular channel URL still works",
			url:  "https://www.youtube.com/channel/UC_test_channel",
			expected: model.Info{
				LinkType: model.TypeChannel,
				Provider: model.ProviderYoutube,
				ItemID:   "UC_test_channel",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseURL(tt.url)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected.LinkType, result.LinkType)
			require.Equal(t, tt.expected.Provider, result.Provider)
			require.Equal(t, tt.expected.ItemID, result.ItemID)
		})
	}
}
