// Package metrics provides optional Prometheus instrumentation for Podsync.
//
// A single *Metrics value is created at startup and threaded through the
// feed updater and the web server. Every recording method is safe to call on
// a nil *Metrics, so callers never need to guard against metrics being
// disabled. The collected series are exposed via the opt-in /metrics HTTP
// endpoint (see services/web).
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// namespace prefixes every metric exported by Podsync.
const namespace = "podsync"

// Artifact labels for enrichment metrics.
const (
	ArtifactTranscript = "transcript"
	ArtifactChapters   = "chapters"
)

// Outcome labels shared across counters.
const (
	OutcomeProduced = "produced"
	OutcomeEmpty    = "empty"
)

// Result labels shared across counters (feed updates, downloads, API calls).
const (
	ResultSuccess     = "success"
	ResultError       = "error"
	ResultRateLimited = "rate_limited"
)

// Metrics holds the Prometheus collectors exported by Podsync. The zero value
// is not usable; construct one with New. A nil *Metrics is a valid no-op
// receiver, which lets callers treat metrics as always-on without nil checks.
type Metrics struct {
	registry *prometheus.Registry

	feedUpdateTotal    *prometheus.CounterVec
	feedUpdateDuration *prometheus.HistogramVec
	downloadTotal      *prometheus.CounterVec
	downloadDuration   *prometheus.HistogramVec
	queueDepth         prometheus.Gauge
	apiQuotaTotal      *prometheus.CounterVec
	apiRequestTotal    *prometheus.CounterVec
	enrichmentTotal    *prometheus.CounterVec
	enrichmentSource   *prometheus.CounterVec
	enrichmentErrors   *prometheus.CounterVec
}

// New builds the collector set on a private registry and registers the Go
// runtime and process collectors alongside it. Using a private registry keeps
// repeated construction (for example in tests) free of duplicate-registration
// panics.
func New() *Metrics {
	// Durations here span from a few seconds (metadata refresh) to tens of
	// minutes (large downloads), so the default sub-10s buckets are useless.
	durationBuckets := prometheus.ExponentialBuckets(1, 2, 12) // 1s .. ~34m

	m := &Metrics{
		registry: prometheus.NewRegistry(),

		feedUpdateTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "feed_update_total",
			Help:      "Total number of feed update cycles by feed and result.",
		}, []string{"feed_id", "result"}),

		feedUpdateDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "feed_update_duration_seconds",
			Help:      "Wall-clock duration of a full feed update cycle.",
			Buckets:   durationBuckets,
		}, []string{"feed_id"}),

		downloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "episode_download_total",
			Help:      "Total number of episode download attempts by feed and result.",
		}, []string{"feed_id", "result"}),

		downloadDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "episode_download_duration_seconds",
			Help:      "Time spent downloading a single episode.",
			Buckets:   durationBuckets,
		}, []string{"feed_id"}),

		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "update_queue_depth",
			Help:      "Number of feeds currently waiting in the update queue.",
		}),

		apiQuotaTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "api_quota_units_total",
			Help:      "Estimated provider API quota units consumed.",
		}, []string{"provider"}),

		apiRequestTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "api_requests_total",
			Help:      "Total number of provider API requests by result.",
		}, []string{"provider", "result"}),

		enrichmentTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "enrichment_total",
			Help:      "Enrichment outcomes by feed, artifact and outcome (produced/empty).",
		}, []string{"feed_id", "artifact", "outcome"}),

		enrichmentSource: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "enrichment_source_total",
			Help:      "Source of produced enrichment artifacts by feed and artifact.",
		}, []string{"feed_id", "artifact", "source"}),

		enrichmentErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "enrichment_errors_total",
			Help:      "Total number of feeds whose enrichment finished with an error.",
		}, []string{"feed_id"}),
	}

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.feedUpdateTotal,
		m.feedUpdateDuration,
		m.downloadTotal,
		m.downloadDuration,
		m.queueDepth,
		m.apiQuotaTotal,
		m.apiRequestTotal,
		m.enrichmentTotal,
		m.enrichmentSource,
		m.enrichmentErrors,
	)

	return m
}

// Handler returns the HTTP handler that serves the Prometheus exposition
// format for this collector set. It returns nil when m is nil.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return nil
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// FeedUpdated records the outcome and duration of a full feed update cycle.
func (m *Metrics) FeedUpdated(feedID string, err error, elapsed time.Duration) {
	if m == nil {
		return
	}
	result := ResultSuccess
	if err != nil {
		result = ResultError
	}
	m.feedUpdateTotal.WithLabelValues(feedID, result).Inc()
	m.feedUpdateDuration.WithLabelValues(feedID).Observe(elapsed.Seconds())
}

// EpisodeDownloaded records the outcome and duration of a single episode
// download. Pass ResultSuccess, ResultError or ResultRateLimited as result.
func (m *Metrics) EpisodeDownloaded(feedID, result string, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.downloadTotal.WithLabelValues(feedID, result).Inc()
	// A rate-limited attempt never ran, so its duration would skew the
	// histogram towards zero; only record durations for attempts that ran.
	if result != ResultRateLimited {
		m.downloadDuration.WithLabelValues(feedID).Observe(elapsed.Seconds())
	}
}

// SetQueueDepth reports the number of feeds waiting in the update queue.
func (m *Metrics) SetQueueDepth(depth int) {
	if m == nil {
		return
	}
	m.queueDepth.Set(float64(depth))
}

// AddAPIQuota records estimated API quota units consumed by a provider.
// It implements the builder.APIRecorder interface.
func (m *Metrics) AddAPIQuota(provider string, units float64) {
	if m == nil {
		return
	}
	m.apiQuotaTotal.WithLabelValues(provider).Add(units)
}

// AddAPIRequest records a single provider API request and whether it
// succeeded. It implements the builder.APIRecorder interface.
func (m *Metrics) AddAPIRequest(provider string, success bool) {
	if m == nil {
		return
	}
	result := ResultSuccess
	if !success {
		result = ResultError
	}
	m.apiRequestTotal.WithLabelValues(provider, result).Inc()
}

// EnrichmentOutcome records whether an enrichment artifact was produced or
// came back empty for a feed. Use ArtifactTranscript/ArtifactChapters and
// OutcomeProduced/OutcomeEmpty.
func (m *Metrics) EnrichmentOutcome(feedID, artifact, outcome string) {
	if m == nil {
		return
	}
	m.enrichmentTotal.WithLabelValues(feedID, artifact, outcome).Inc()
}

// EnrichmentSource records which source produced an enrichment artifact
// (for example "platform", "description", "llm" or "stt:openai").
func (m *Metrics) EnrichmentSource(feedID, artifact, source string) {
	if m == nil || source == "" {
		return
	}
	m.enrichmentSource.WithLabelValues(feedID, artifact, source).Inc()
}

// EnrichmentError records that a feed's enrichment finished with an error.
func (m *Metrics) EnrichmentError(feedID string) {
	if m == nil {
		return
	}
	m.enrichmentErrors.WithLabelValues(feedID).Inc()
}
