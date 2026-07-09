package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exposition renders the current metrics in the Prometheus text format.
func exposition(t *testing.T, m *Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	return rec.Body.String()
}

func TestRecordsFeedAndDownloadMetrics(t *testing.T) {
	m := New()

	m.FeedUpdated("feed1", nil, 3*time.Second)
	m.FeedUpdated("feed1", assertErr("boom"), time.Second)
	m.EpisodeDownloaded("feed1", ResultSuccess, 10*time.Second)
	m.EpisodeDownloaded("feed1", ResultError, time.Second)
	m.EpisodeDownloaded("feed1", ResultRateLimited, 0)
	m.SetQueueDepth(4)

	body := exposition(t, m)

	assert.Contains(t, body, `podsync_feed_update_total{feed_id="feed1",result="success"} 1`)
	assert.Contains(t, body, `podsync_feed_update_total{feed_id="feed1",result="error"} 1`)
	assert.Contains(t, body, `podsync_episode_download_total{feed_id="feed1",result="success"} 1`)
	assert.Contains(t, body, `podsync_episode_download_total{feed_id="feed1",result="rate_limited"} 1`)
	assert.Contains(t, body, `podsync_update_queue_depth 4`)
	assert.Contains(t, body, "podsync_feed_update_duration_seconds")
	// The rate-limited attempt never ran, so only two download durations
	// (success + error) should be observed.
	assert.Contains(t, body, `podsync_episode_download_duration_seconds_count{feed_id="feed1"} 2`)
}

func TestRecordsAPIAndEnrichmentMetrics(t *testing.T) {
	m := New()

	m.AddAPIQuota("youtube", 5)
	m.AddAPIQuota("youtube", 3)
	m.AddAPIRequest("youtube", true)
	m.AddAPIRequest("youtube", false)

	m.EnrichmentOutcome("feed1", ArtifactTranscript, OutcomeProduced)
	m.EnrichmentSource("feed1", ArtifactTranscript, "platform")
	m.EnrichmentSource("feed1", ArtifactChapters, "") // empty source ignored
	m.EnrichmentOutcome("feed1", ArtifactChapters, OutcomeEmpty)
	m.EnrichmentError("feed1")

	body := exposition(t, m)

	assert.Contains(t, body, `podsync_api_quota_units_total{provider="youtube"} 8`)
	assert.Contains(t, body, `podsync_api_requests_total{provider="youtube",result="success"} 1`)
	assert.Contains(t, body, `podsync_api_requests_total{provider="youtube",result="error"} 1`)
	assert.Contains(t, body, `podsync_enrichment_total{artifact="transcript",feed_id="feed1",outcome="produced"} 1`)
	assert.Contains(t, body, `podsync_enrichment_source_total{artifact="transcript",feed_id="feed1",source="platform"} 1`)
	assert.Contains(t, body, `podsync_enrichment_errors_total{feed_id="feed1"} 1`)
	// An empty source must not create a series.
	assert.NotContains(t, body, `source=""`)
}

func TestNilMetricsAreNoOps(t *testing.T) {
	var m *Metrics

	// None of these should panic on a nil receiver.
	assert.NotPanics(t, func() {
		m.FeedUpdated("feed1", nil, time.Second)
		m.EpisodeDownloaded("feed1", ResultSuccess, time.Second)
		m.SetQueueDepth(1)
		m.AddAPIQuota("youtube", 5)
		m.AddAPIRequest("youtube", true)
		m.EnrichmentOutcome("feed1", ArtifactTranscript, OutcomeProduced)
		m.EnrichmentSource("feed1", ArtifactTranscript, "platform")
		m.EnrichmentError("feed1")
	})

	assert.Nil(t, m.Handler())
}

func TestExpositionCarriesRuntimeCollectors(t *testing.T) {
	m := New()
	body := exposition(t, m)
	// Go and process collectors should be registered for baseline observability.
	assert.True(t, strings.Contains(body, "go_goroutines"))
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
