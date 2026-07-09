package builder

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestResolveHandleUsesSharedCacheWhenHandlesNil(t *testing.T) {
	const channelsPath = "/youtube/v3/channels"

	transport := &MockTransport{
		responses: map[string]string{channelsPath: `{"items": [{"id": "UC_shared_cache"}]}`},
	}
	yt := newTestYouTubeBuilder(t, transport)
	yt.handles = nil

	channelID, err := yt.resolveHandle(context.Background(), "sharedhandle")
	require.NoError(t, err)
	require.Equal(t, "UC_shared_cache", channelID)

	cachedID, ok := sharedHandleCache.get("sharedhandle")
	require.True(t, ok)
	require.Equal(t, channelID, cachedID)
}

func TestKeepLastPlaylistSnippetsCopiesBackingArray(t *testing.T) {
	snippets := []*youtube.PlaylistItemSnippet{
		{Title: "one"},
		{Title: "two"},
		{Title: "three"},
		{Title: "four"},
	}

	kept := keepLastPlaylistSnippets(snippets, 2)

	require.Len(t, kept, 2)
	require.Equal(t, 2, cap(kept))
	require.Equal(t, "three", kept[0].Title)
	require.Equal(t, "four", kept[1].Title)

	snippets[2] = &youtube.PlaylistItemSnippet{Title: "replaced"}
	require.Equal(t, "three", kept[0].Title)
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

func TestQuotaCost(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  float64
	}{
		{name: "handle lookup (id only)", parts: []string{"id"}, want: 1},
		{name: "channel metadata", parts: []string{"id", "snippet", "contentDetails"}, want: 5},
		{name: "channel statistics", parts: []string{"id", "statistics"}, want: 3},
		{name: "playlist snippet", parts: []string{"id", "snippet"}, want: 3},
		{name: "video descriptions", parts: []string{"id", "snippet", "contentDetails"}, want: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, quotaCost(tt.parts))
		})
	}
}

type recordingAPI struct {
	quota    float64
	requests int
	failures int
}

func (r *recordingAPI) AddAPIQuota(_ string, units float64) { r.quota += units }
func (r *recordingAPI) AddAPIRequest(_ string, success bool) {
	r.requests++
	if !success {
		r.failures++
	}
}

func TestRecordAPIReportsQuotaAndRequests(t *testing.T) {
	rec := &recordingAPI{}
	yt := &YouTubeBuilder{recorder: rec}

	yt.recordAPI([]string{"id", "snippet", "contentDetails"}, nil)
	yt.recordAPI([]string{"id"}, assert.AnError)

	require.Equal(t, float64(6), rec.quota) // 5 + 1
	require.Equal(t, 2, rec.requests)
	require.Equal(t, 1, rec.failures)

	// A nil recorder must be a safe no-op.
	require.NotPanics(t, func() {
		(&YouTubeBuilder{}).recordAPI([]string{"id"}, nil)
	})
}
