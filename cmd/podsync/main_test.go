package main

import (
	"context"
	"net/http"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
)

type fakeUpdateRunner struct {
	mu       sync.Mutex
	calls    []string
	onUpdate func(feedConfig *feed.Config)
}

func (r *fakeUpdateRunner) Update(_ context.Context, feedConfig *feed.Config) error {
	r.mu.Lock()
	r.calls = append(r.calls, feedConfig.ID)
	r.mu.Unlock()

	if r.onUpdate != nil {
		r.onUpdate(feedConfig)
	}
	return nil
}

func (r *fakeUpdateRunner) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	calls := make([]string, len(r.calls))
	copy(calls, r.calls)
	return calls
}

type fakeServiceServer struct {
	startedOnce  sync.Once
	shutdownOnce sync.Once
	started      chan struct{}
	shutdown     chan struct{}

	mu            sync.Mutex
	listenCalls   int
	tlsCalls      int
	shutdownCalls int
	certFile      string
	keyFile       string
}

func newFakeServiceServer() *fakeServiceServer {
	return &fakeServiceServer{
		started:  make(chan struct{}),
		shutdown: make(chan struct{}),
	}
}

func (s *fakeServiceServer) ListenAndServe() error {
	s.mu.Lock()
	s.listenCalls++
	s.mu.Unlock()

	s.startedOnce.Do(func() { close(s.started) })
	<-s.shutdown
	return http.ErrServerClosed
}

func (s *fakeServiceServer) ListenAndServeTLS(certFile, keyFile string) error {
	s.mu.Lock()
	s.tlsCalls++
	s.certFile = certFile
	s.keyFile = keyFile
	s.mu.Unlock()

	s.startedOnce.Do(func() { close(s.started) })
	<-s.shutdown
	return http.ErrServerClosed
}

func (s *fakeServiceServer) Shutdown(context.Context) error {
	s.mu.Lock()
	s.shutdownCalls++
	s.mu.Unlock()

	s.shutdownOnce.Do(func() { close(s.shutdown) })
	return nil
}

func (s *fakeServiceServer) snapshot() (listenCalls, tlsCalls, shutdownCalls int, certFile, keyFile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listenCalls, s.tlsCalls, s.shutdownCalls, s.certFile, s.keyFile
}

func requireShutdownError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.True(t, err == context.Canceled || err == http.ErrServerClosed, "unexpected error: %v", err)
}

func TestRunServiceQueuesInitialUpdatesOnlyForIntervalFeeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updater := &fakeUpdateRunner{
		onUpdate: func(*feed.Config) {
			cancel()
		},
	}

	feeds := map[string]*feed.Config{
		"interval": {
			ID:           "interval",
			URL:          "https://youtube.com/channel/interval",
			UpdatePeriod: time.Hour,
		},
		"scheduled": {
			ID:           "scheduled",
			URL:          "https://youtube.com/channel/scheduled",
			CronSchedule: "0 0 1 1 *",
			UpdatePeriod: time.Hour,
		},
	}

	err := runService(ctx, serviceConfig{
		Feeds:     feeds,
		Manager:   updater,
		QueueSize: 2,
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"interval"}, updater.Calls())
	assert.Equal(t, "@every 1h0m0s", feeds["interval"].CronSchedule)
	assert.Equal(t, "0 0 1 1 *", feeds["scheduled"].CronSchedule)
}

func TestRunServiceCronScheduleEnqueuesExplicitFeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var once sync.Once
	updater := &fakeUpdateRunner{
		onUpdate: func(*feed.Config) {
			once.Do(cancel)
		},
	}

	err := runService(ctx, serviceConfig{
		Feeds: map[string]*feed.Config{
			"cron": {
				ID:           "cron",
				URL:          "https://youtube.com/channel/cron",
				CronSchedule: "@every 10ms",
				UpdatePeriod: time.Hour,
			},
		},
		Manager:   updater,
		QueueSize: 1,
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, updater.Calls(), "cron")
}

func TestRunServiceReturnsInvalidCronSchedule(t *testing.T) {
	err := runService(context.Background(), serviceConfig{
		Feeds: map[string]*feed.Config{
			"bad": {
				ID:           "bad",
				URL:          "https://youtube.com/channel/bad",
				CronSchedule: "not a schedule",
			},
		},
		Manager: &fakeUpdateRunner{},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "can't create cron task for feed bad")
}

func TestRunServiceSignalShutsDownWebServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	stop := make(chan os.Signal, 1)
	server := newFakeServiceServer()
	done := make(chan error, 1)

	go func() {
		done <- runService(ctx, serviceConfig{
			Feeds:      map[string]*feed.Config{},
			Manager:    &fakeUpdateRunner{},
			Server:     server,
			ServerAddr: "127.0.0.1:0",
			Stop:       stop,
		})
	}()

	select {
	case <-server.started:
	case <-ctx.Done():
		t.Fatal("server did not start")
	}

	stop <- syscall.SIGTERM

	select {
	case err := <-done:
		requireShutdownError(t, err)
	case <-ctx.Done():
		t.Fatal("service did not stop")
	}

	listenCalls, tlsCalls, shutdownCalls, _, _ := server.snapshot()
	assert.Equal(t, 1, listenCalls)
	assert.Equal(t, 0, tlsCalls)
	assert.Equal(t, 1, shutdownCalls)
}

func TestRunServiceUsesTLSListenerWhenConfigured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	stop := make(chan os.Signal, 1)
	server := newFakeServiceServer()
	done := make(chan error, 1)

	go func() {
		done <- runService(ctx, serviceConfig{
			Feeds:           map[string]*feed.Config{},
			Manager:         &fakeUpdateRunner{},
			Server:          server,
			ServerAddr:      "127.0.0.1:0",
			TLS:             true,
			CertificatePath: "test-cert.pem",
			KeyFilePath:     "test-key.pem",
			Stop:            stop,
		})
	}()

	select {
	case <-server.started:
	case <-ctx.Done():
		t.Fatal("server did not start")
	}

	stop <- syscall.SIGTERM

	select {
	case err := <-done:
		requireShutdownError(t, err)
	case <-ctx.Done():
		t.Fatal("service did not stop")
	}

	listenCalls, tlsCalls, shutdownCalls, certFile, keyFile := server.snapshot()
	assert.Equal(t, 0, listenCalls)
	assert.Equal(t, 1, tlsCalls)
	assert.Equal(t, 1, shutdownCalls)
	assert.Equal(t, "test-cert.pem", certFile)
	assert.Equal(t, "test-key.pem", keyFile)
}
