package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/builder"
	"github.com/mxpv/podsync/pkg/model"
)

type mockSearcher struct {
	resolveCalls int
	searchCalls  int
	lastInfo     model.Info
	lastQuery    string
	resolveErr   error
}

func (m *mockSearcher) Resolve(_ context.Context, info model.Info) (*builder.SearchResult, error) {
	m.resolveCalls++
	m.lastInfo = info

	if m.resolveErr != nil {
		return nil, m.resolveErr
	}

	return &builder.SearchResult{
		Provider:  model.ProviderYoutube,
		Type:      model.TypeChannel,
		ID:        "UC1234567890",
		Title:     "Test Channel!",
		Thumbnail: "https://example.com/thumb.jpg",
		URL:       "https://youtube.com/channel/UC1234567890",
	}, nil
}

func (m *mockSearcher) Search(_ context.Context, query string, _ int64) ([]builder.SearchResult, error) {
	m.searchCalls++
	m.lastQuery = query

	return []builder.SearchResult{
		{
			Provider: model.ProviderYoutube,
			Type:     model.TypeChannel,
			ID:       "UC1234567890",
			Title:    "Test Channel!",
			URL:      "https://youtube.com/channel/UC1234567890",
		},
		{
			Provider: model.ProviderYoutube,
			Type:     model.TypePlaylist,
			ID:       "PL0987654321",
			Title:    "Test Playlist",
			URL:      "https://youtube.com/playlist?list=PL0987654321",
		},
	}, nil
}

func doSearchRequest(t *testing.T, srv *Server, target string) (*httptest.ResponseRecorder, SearchResponse) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	var resp SearchResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}

	return rec, resp
}

func TestSearchDisabledByDefault(t *testing.T) {
	cfg := Config{Port: 8080, Path: "feeds"}

	srv := New(cfg, &mockFileSystem{}, nil, nil)

	rec, _ := doSearchRequest(t, srv, "/search?q=test")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSearchRequiresQuery(t *testing.T) {
	cfg := Config{Port: 8080, SearchEnabled: true}

	srv := New(cfg, &mockFileSystem{}, nil, &mockSearcher{})

	rec, _ := doSearchRequest(t, srv, "/search")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSearchResolvesURL(t *testing.T) {
	cfg := Config{Port: 8080, Hostname: "https://example.com/", SearchEnabled: true}
	searcher := &mockSearcher{}

	srv := New(cfg, &mockFileSystem{}, nil, searcher)

	rec, resp := doSearchRequest(t, srv, "/search?q="+`https%3A%2F%2Fyoutube.com%2Fchannel%2FUC1234567890`)
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, resp.Results, 1)
	assert.Equal(t, 1, searcher.resolveCalls)
	assert.Equal(t, model.TypeChannel, searcher.lastInfo.LinkType)
	assert.Equal(t, "UC1234567890", searcher.lastInfo.ItemID)

	result := resp.Results[0]
	assert.Equal(t, "Test Channel!", result.Title)
	assert.Equal(t, "test_channel", result.FeedID)
	assert.Equal(t, "https://example.com/test_channel.xml", result.FeedURL)
}

func TestSearchResolvesHandle(t *testing.T) {
	cfg := Config{Port: 8080, Hostname: "https://example.com", SearchEnabled: true}
	searcher := &mockSearcher{}

	srv := New(cfg, &mockFileSystem{}, nil, searcher)

	rec, resp := doSearchRequest(t, srv, "/search?q=%40veritasium")
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, resp.Results, 1)
	assert.Equal(t, model.TypeHandle, searcher.lastInfo.LinkType)
	assert.Equal(t, "veritasium", searcher.lastInfo.ItemID)
}

func TestSearchNotFoundReturnsEmptyResults(t *testing.T) {
	cfg := Config{Port: 8080, SearchEnabled: true}
	searcher := &mockSearcher{resolveErr: model.ErrNotFound}

	srv := New(cfg, &mockFileSystem{}, nil, searcher)

	rec, resp := doSearchRequest(t, srv, "/search?q=%40nosuchhandle")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, resp.Results)
}

func TestSearchKeywordDisabledByDefault(t *testing.T) {
	cfg := Config{Port: 8080, SearchEnabled: true}
	searcher := &mockSearcher{}

	srv := New(cfg, &mockFileSystem{}, nil, searcher)

	rec, _ := doSearchRequest(t, srv, "/search?q=veritasium")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 0, searcher.searchCalls)
}

func TestSearchKeywordEnabled(t *testing.T) {
	cfg := Config{Port: 8080, Hostname: "https://example.com", SearchEnabled: true, SearchUseAPI: true}
	searcher := &mockSearcher{}

	srv := New(cfg, &mockFileSystem{}, nil, searcher)

	rec, resp := doSearchRequest(t, srv, "/search?q=test+channel")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, searcher.searchCalls)
	assert.Equal(t, "test channel", searcher.lastQuery)

	require.Len(t, resp.Results, 2)
	assert.Equal(t, "https://example.com/test_channel.xml", resp.Results[0].FeedURL)
	assert.Equal(t, "https://example.com/test_playlist.xml", resp.Results[1].FeedURL)
}

func TestSearchCachesResults(t *testing.T) {
	cfg := Config{Port: 8080, SearchEnabled: true, SearchUseAPI: true}
	searcher := &mockSearcher{}

	srv := New(cfg, &mockFileSystem{}, nil, searcher)

	for i := 0; i < 3; i++ {
		rec, _ := doSearchRequest(t, srv, "/search?q=test+channel")
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	assert.Equal(t, 1, searcher.searchCalls)
}

func TestSearchWithoutSearcherReturnsBareResult(t *testing.T) {
	cfg := Config{Port: 8080, Hostname: "https://example.com", SearchEnabled: true}

	srv := New(cfg, &mockFileSystem{}, nil, nil)

	rec, resp := doSearchRequest(t, srv, "/search?q=youtube.com%2Fplaylist%3Flist%3DPL123")
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, resp.Results, 1)
	result := resp.Results[0]
	assert.Equal(t, model.TypePlaylist, result.Type)
	assert.Equal(t, "PL123", result.ID)
	assert.Empty(t, result.Title)
	assert.Equal(t, "https://youtube.com/playlist?list=PL123", result.URL)
	assert.Equal(t, "https://example.com/pl123.xml", result.FeedURL)

	// Keyword search without a searcher is unavailable even when enabled
	cfg.SearchUseAPI = true
	srv = New(cfg, &mockFileSystem{}, nil, nil)
	rec, _ = doSearchRequest(t, srv, "/search?q=some+keywords")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestSearchAvailableUnderServerPath(t *testing.T) {
	cfg := Config{Port: 8080, Path: "feeds", SearchEnabled: true}
	searcher := &mockSearcher{}

	srv := New(cfg, &mockFileSystem{}, nil, searcher)

	rec, resp := doSearchRequest(t, srv, "/feeds/search?q=%40veritasium")
	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, resp.Results, 1)
}

func TestSuggestFeedID(t *testing.T) {
	for input, expected := range map[string]string{
		"Test Channel!":     "test_channel",
		"  Veritasium  ":    "veritasium",
		"MKBHD 4K (extras)": "mkbhd_4k_extras",
		"日本語のみ":             "feed",
	} {
		assert.Equal(t, expected, suggestFeedID(builder.SearchResult{Title: input}), "input: %q", input)
	}

	// Falls back to ID when there is no title
	assert.Equal(t, "uc123", suggestFeedID(builder.SearchResult{ID: "UC123"}))
}
