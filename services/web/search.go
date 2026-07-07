package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/builder"
	"github.com/mxpv/podsync/pkg/model"
)

const (
	searchMaxResults   = 10
	searchCacheTTL     = 24 * time.Hour
	searchCacheMaxSize = 256
)

// Searcher resolves URLs/handles and performs keyword search for feed sources.
type Searcher interface {
	Resolve(ctx context.Context, info model.Info) (*builder.SearchResult, error)
	Search(ctx context.Context, query string, limit int64) ([]builder.SearchResult, error)
}

// SearchFeed is a search result annotated with the feed URL Podsync would produce for it.
type SearchFeed struct {
	builder.SearchResult
	FeedID  string `json:"feed_id"`
	FeedURL string `json:"feed_url"`
}

type SearchResponse struct {
	Query   string       `json:"query"`
	Results []SearchFeed `json:"results"`
}

type searchError struct {
	status  int
	message string
}

func (e *searchError) Error() string {
	return e.message
}

type searchService struct {
	searcher Searcher
	hostname string
	useAPI   bool

	mu    sync.Mutex
	cache map[string]searchCacheEntry
	order []string
}

type searchCacheEntry struct {
	results []SearchFeed
	expires time.Time
}

func newSearchService(searcher Searcher, hostname string, useAPI bool) *searchService {
	return &searchService{
		searcher: searcher,
		hostname: hostname,
		useAPI:   useAPI,
		cache:    make(map[string]searchCacheEntry),
	}
}

func (s *searchService) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeSearchError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}

	results, err := s.lookup(r.Context(), query)
	if err != nil {
		var searchErr *searchError
		if errors.As(err, &searchErr) {
			writeSearchError(w, searchErr.status, searchErr.message)
			return
		}

		log.WithError(err).WithField("query", query).Error("search request failed")
		writeSearchError(w, http.StatusBadGateway, "failed to query the provider API")
		return
	}

	json.NewEncoder(w).Encode(SearchResponse{Query: query, Results: results})
}

func (s *searchService) lookup(ctx context.Context, query string) ([]SearchFeed, error) {
	if results, ok := s.cacheGet(query); ok {
		return results, nil
	}

	results, err := s.query(ctx, query)
	if err != nil {
		return nil, err
	}

	s.cachePut(query, results)
	return results, nil
}

func (s *searchService) query(ctx context.Context, query string) ([]SearchFeed, error) {
	info, direct := classifyQuery(query)
	if !direct {
		// Keyword search costs 100 YouTube API units per call, so it must be enabled explicitly.
		if !s.useAPI {
			return nil, &searchError{
				status:  http.StatusForbidden,
				message: "keyword search is disabled, enable server.search_use_api or paste a channel/playlist URL",
			}
		}

		if s.searcher == nil {
			return nil, &searchError{
				status:  http.StatusServiceUnavailable,
				message: "keyword search requires a YouTube API key",
			}
		}

		found, err := s.searcher.Search(ctx, query, searchMaxResults)
		if err != nil {
			return nil, err
		}

		return s.decorate(found), nil
	}

	// Without an API key we can still return the parsed reference, just without metadata.
	// Non-YouTube URLs are returned as-is: search only enriches YouTube sources for now.
	if s.searcher == nil || info.Provider != model.ProviderYoutube {
		return s.decorate([]builder.SearchResult{bareResult(info, query)}), nil
	}

	resolved, err := s.searcher.Resolve(ctx, info)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return []SearchFeed{}, nil
		}

		return nil, err
	}

	return s.decorate([]builder.SearchResult{*resolved}), nil
}

// classifyQuery decides whether the query is a direct reference (URL or @handle)
// or a keyword search phrase.
func classifyQuery(query string) (model.Info, bool) {
	if strings.HasPrefix(query, "@") && !strings.ContainsAny(query, " /?&") {
		return model.Info{
			Provider: model.ProviderYoutube,
			LinkType: model.TypeHandle,
			ItemID:   strings.TrimPrefix(query, "@"),
		}, true
	}

	if info, err := builder.ParseURL(query); err == nil {
		return info, true
	}

	return model.Info{}, false
}

// bareResult builds a result from URL parsing alone, without querying any API.
func bareResult(info model.Info, query string) builder.SearchResult {
	result := builder.SearchResult{
		Provider: info.Provider,
		Type:     info.LinkType,
		ID:       info.ItemID,
	}

	if info.Provider == model.ProviderYoutube {
		result.URL = builder.YouTubeCanonicalURL(info.LinkType, info.ItemID)
	} else {
		result.URL = query
		if !strings.HasPrefix(result.URL, "http") {
			result.URL = "https://" + result.URL
		}
	}

	return result
}

func (s *searchService) decorate(results []builder.SearchResult) []SearchFeed {
	feeds := make([]SearchFeed, 0, len(results))
	for _, result := range results {
		feedID := suggestFeedID(result)
		feeds = append(feeds, SearchFeed{
			SearchResult: result,
			FeedID:       feedID,
			FeedURL:      strings.TrimRight(s.hostname, "/") + "/" + feedID + ".xml",
		})
	}

	return feeds
}

// suggestFeedID derives a config-friendly feed ID ([a-z0-9_]) from the result title or ID.
func suggestFeedID(result builder.SearchResult) string {
	base := result.Title
	if base == "" {
		base = result.ID
	}

	var (
		b   strings.Builder
		gap bool
	)

	for _, c := range strings.ToLower(base) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if gap && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(c)
			gap = false
		} else {
			gap = true
		}
	}

	if b.Len() == 0 {
		return "feed"
	}

	return b.String()
}

func (s *searchService) cacheGet(query string) ([]SearchFeed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[query]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}

	return entry.results, true
}

func (s *searchService) cachePut(query string, results []SearchFeed) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.cache[query]; !ok {
		if len(s.order) >= searchCacheMaxSize {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.cache, oldest)
		}
		s.order = append(s.order, query)
	}

	s.cache[query] = searchCacheEntry{results: results, expires: time.Now().Add(searchCacheTTL)}
}

func writeSearchError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
