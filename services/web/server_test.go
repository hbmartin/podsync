package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/metrics"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockFileSystem struct{}

func (m *mockFileSystem) Open(name string) (http.File, error) {
	return nil, http.ErrMissingFile
}

type healthDB struct {
	feeds           []*model.Feed
	episodes        map[string][]*model.Episode
	walkFeedsErr    error
	walkEpisodesErr error
}

func (d *healthDB) Close() error          { return nil }
func (d *healthDB) Version() (int, error) { return 1, nil }

func (d *healthDB) AddFeed(context.Context, string, *model.Feed) error { return nil }

func (d *healthDB) GetFeed(context.Context, string) (*model.Feed, error) {
	return nil, model.ErrNotFound
}

func (d *healthDB) WalkFeeds(_ context.Context, cb func(feed *model.Feed) error) error {
	if d.walkFeedsErr != nil {
		return d.walkFeedsErr
	}
	for _, feed := range d.feeds {
		if err := cb(feed); err != nil {
			return err
		}
	}
	return nil
}

func (d *healthDB) DeleteFeed(context.Context, string) error { return nil }

func (d *healthDB) GetEpisode(context.Context, string, string) (*model.Episode, error) {
	return nil, model.ErrNotFound
}

func (d *healthDB) UpdateEpisode(string, string, func(episode *model.Episode) error) error {
	return nil
}

func (d *healthDB) DeleteEpisode(string, string) error { return nil }

func (d *healthDB) WalkEpisodes(_ context.Context, feedID string, cb func(episode *model.Episode) error) error {
	if d.walkEpisodesErr != nil {
		return d.walkEpisodesErr
	}
	for _, episode := range d.episodes[feedID] {
		if err := cb(episode); err != nil {
			return err
		}
	}
	return nil
}

func TestDebugEndpointDisabledByDefault(t *testing.T) {
	cfg := Config{
		Port: 8080,
		Path: "feeds",
	}

	srv := New(cfg, &mockFileSystem{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/debug/vars", nil)
	rec := httptest.NewRecorder()

	srv.Handler.ServeHTTP(rec, req)

	// Should return 404 when debug endpoints are disabled
	assert.Equal(t, http.StatusNotFound, rec.Code)
	// Should NOT contain expvar data
	assert.False(t, strings.Contains(rec.Body.String(), "cmdline"))
}

func TestDebugEndpointEnabledWhenConfigured(t *testing.T) {
	cfg := Config{
		Port:           8080,
		Path:           "feeds",
		DebugEndpoints: true,
	}

	srv := New(cfg, &mockFileSystem{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/debug/vars", nil)
	rec := httptest.NewRecorder()

	srv.Handler.ServeHTTP(rec, req)

	// Should return 200 and JSON content when debug endpoints are enabled
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	// Verify it contains expvar data (cmdline is always present)
	assert.True(t, strings.Contains(rec.Body.String(), "cmdline"))
}

func TestMetricsEndpointDisabledByDefault(t *testing.T) {
	cfg := Config{
		Port: 8080,
		Path: "feeds",
	}

	srv := New(cfg, &mockFileSystem{}, nil, metrics.New())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	// Should return 404 when the metrics endpoint is disabled
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMetricsEndpointEnabledWhenConfigured(t *testing.T) {
	cfg := Config{
		Port:    8080,
		Path:    "feeds",
		Metrics: true,
	}

	srv := New(cfg, &mockFileSystem{}, nil, metrics.New())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	// Prometheus exposition always carries the Go runtime collectors.
	assert.Contains(t, rec.Body.String(), "go_goroutines")
}

func TestMetricsEndpointNoCollector(t *testing.T) {
	cfg := Config{
		Port:    8080,
		Path:    "feeds",
		Metrics: true,
	}

	// Enabling the flag without a collector must not register the endpoint
	// and must not panic.
	srv := New(cfg, &mockFileSystem{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNoIndexDisabledByDefault(t *testing.T) {
	cfg := Config{
		Port: 8080,
		Path: "feeds",
	}

	srv := New(cfg, &mockFileSystem{}, nil, nil)

	// robots.txt should return 404 when disabled
	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// X-Robots-Tag header should not be present on feed requests
	req = httptest.NewRequest(http.MethodGet, "/feeds/test.xml", nil)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Empty(t, rec.Header().Get("X-Robots-Tag"))
}

func TestNoIndexEnabledWhenConfigured(t *testing.T) {
	cfg := Config{
		Port:    8080,
		Path:    "feeds",
		NoIndex: true,
	}

	srv := New(cfg, &mockFileSystem{}, nil, nil)

	// robots.txt should return disallow all
	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "User-agent: *")
	assert.Contains(t, rec.Body.String(), "Disallow: /")

	// X-Robots-Tag header should be present on all responses
	req = httptest.NewRequest(http.MethodGet, "/feeds/test.xml", nil)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, "noindex, nofollow", rec.Header().Get("X-Robots-Tag"))
}

func TestNoListingDisabledByDefault(t *testing.T) {
	tmpDir := t.TempDir()

	// Create storage with NoListing disabled (default)
	storage, err := fs.NewLocal(tmpDir, false, false)
	require.NoError(t, err)

	// Create a file inside a subdirectory
	_, err = storage.Create(context.Background(), "feeds/episode.mp3", bytes.NewReader([]byte("audio content")))
	require.NoError(t, err)

	cfg := Config{
		Port: 8080,
		Path: "",
	}

	srv := New(cfg, storage, nil, nil)

	// Accessing a directory should return 200 with directory listing
	req := httptest.NewRequest(http.MethodGet, "/feeds/", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "episode.mp3")

	// Accessing root should also return 200 with directory listing
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "feeds")

	// Accessing a file should work
	req = httptest.NewRequest(http.MethodGet, "/feeds/episode.mp3", nil)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "audio content", rec.Body.String())
}

func TestHealthCheckHealthy(t *testing.T) {
	database := &healthDB{
		feeds: []*model.Feed{{ID: "feed1"}},
		episodes: map[string][]*model.Episode{
			"feed1": {
				{ID: "ok", Status: model.EpisodeDownloaded, PubDate: time.Now()},
			},
		},
	}

	srv := New(Config{Path: "feeds"}, &mockFileSystem{}, database, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var status HealthStatus
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	assert.Equal(t, "healthy", status.Status)
	assert.Equal(t, "no recent download failures detected", status.Message)
	assert.Zero(t, status.FailedEpisodes)
}

func TestHealthCheckReportsRecentFailures(t *testing.T) {
	database := &healthDB{
		feeds: []*model.Feed{{ID: "feed1"}},
		episodes: map[string][]*model.Episode{
			"feed1": {
				{ID: "recent", Status: model.EpisodeError, PubDate: time.Now()},
				{ID: "old", Status: model.EpisodeError, PubDate: time.Now().Add(-48 * time.Hour)},
			},
		},
	}

	srv := New(Config{Path: "feeds"}, &mockFileSystem{}, database, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var status HealthStatus
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	assert.Equal(t, "unhealthy", status.Status)
	assert.Equal(t, 1, status.FailedEpisodes)
	assert.Contains(t, status.Message, "found 1 failed downloads")
}

func TestHealthCheckReportsDatabaseErrors(t *testing.T) {
	srv := New(Config{Path: "feeds"}, &mockFileSystem{}, &healthDB{
		walkFeedsErr: errors.New("database unavailable"),
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var status HealthStatus
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	assert.Equal(t, "unhealthy", status.Status)
	assert.Equal(t, "database error during health check", status.Message)
}

func TestServerServesFilesOverTLS(t *testing.T) {
	tmpDir := t.TempDir()

	storage, err := fs.NewLocal(tmpDir, false, false)
	require.NoError(t, err)
	_, err = storage.Create(context.Background(), "feeds/episode.mp3", bytes.NewReader([]byte("audio content")))
	require.NoError(t, err)

	certPath, keyPath := writeTestCertificate(t)
	srv := New(Config{Path: ""}, storage, nil, nil)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- srv.ServeTLS(listener, certPath, keyPath)
	}()

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test uses a throwaway self-signed certificate.
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	url := "https://" + listener.Addr().String() + "/feeds/episode.mp3"

	require.Eventually(t, func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)
		return err == nil && resp.StatusCode == http.StatusOK && string(data) == "audio content"
	}, time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))

	select {
	case err := <-done:
		assert.ErrorIs(t, err, http.ErrServerClosed)
	case <-ctx.Done():
		t.Fatal("TLS server did not stop")
	}
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

	return certPath, keyPath
}

func TestNoListingEnabledWhenConfigured(t *testing.T) {
	tmpDir := t.TempDir()

	storage, err := fs.NewLocal(tmpDir, false, true)
	require.NoError(t, err)

	// Create a file inside a subdirectory
	_, err = storage.Create(context.Background(), "feeds/episode.mp3", bytes.NewReader([]byte("audio content")))
	require.NoError(t, err)

	cfg := Config{
		Port: 8080,
		Path: "",
	}

	srv := New(cfg, storage, nil, nil)

	// Accessing a directory should return 404
	req := httptest.NewRequest(http.MethodGet, "/feeds/", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Accessing root should also return 404
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Accessing a file should still work
	req = httptest.NewRequest(http.MethodGet, "/feeds/episode.mp3", nil)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "audio content", rec.Body.String())
}
