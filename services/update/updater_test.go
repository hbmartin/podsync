package update

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
)

// testDB is an in-memory db.Storage implementation. Episodes are walked in
// slice order.
type testDB struct {
	episodes map[string][]*model.Episode
}

func (d *testDB) Close() error          { return nil }
func (d *testDB) Version() (int, error) { return 1, nil }

func (d *testDB) AddFeed(_ context.Context, _ string, _ *model.Feed) error { return nil }

func (d *testDB) GetFeed(_ context.Context, _ string) (*model.Feed, error) {
	return nil, model.ErrNotFound
}

func (d *testDB) WalkFeeds(_ context.Context, _ func(feed *model.Feed) error) error { return nil }

func (d *testDB) DeleteFeed(_ context.Context, _ string) error { return nil }

func (d *testDB) GetEpisode(_ context.Context, feedID string, episodeID string) (*model.Episode, error) {
	for _, episode := range d.episodes[feedID] {
		if episode.ID == episodeID {
			return episode, nil
		}
	}
	return nil, model.ErrNotFound
}

func (d *testDB) UpdateEpisode(feedID string, episodeID string, cb func(episode *model.Episode) error) error {
	for _, episode := range d.episodes[feedID] {
		if episode.ID == episodeID {
			return cb(episode)
		}
	}
	return model.ErrNotFound
}

func (d *testDB) DeleteEpisode(_ string, _ string) error { return nil }

func (d *testDB) WalkEpisodes(_ context.Context, feedID string, cb func(episode *model.Episode) error) error {
	for _, episode := range d.episodes[feedID] {
		if err := cb(episode); err != nil {
			return err
		}
	}
	return nil
}

// testFS is an in-memory fs.Storage implementation.
type testFS struct {
	files   map[string][]byte
	created []string // paths passed to Create, in order
}

func (f *testFS) Open(_ string) (http.File, error) { return nil, os.ErrNotExist }

func (f *testFS) Create(_ context.Context, name string, reader io.Reader) (int64, error) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		return 0, err
	}
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[name] = buf.Bytes()
	f.created = append(f.created, name)
	return int64(buf.Len()), nil
}

func (f *testFS) Delete(_ context.Context, name string) error {
	delete(f.files, name)
	return nil
}

func (f *testFS) Size(_ context.Context, name string) (int64, error) {
	data, ok := f.files[name]
	if !ok {
		return 0, os.ErrNotExist
	}
	return int64(len(data)), nil
}

// testDownloader implements the Downloader interface.
type testDownloader struct {
	download func(ctx context.Context, cfg *feed.Config, episode *model.Episode) (io.ReadCloser, error)
}

func (d *testDownloader) Download(ctx context.Context, cfg *feed.Config, episode *model.Episode) (io.ReadCloser, error) {
	return d.download(ctx, cfg, episode)
}

func (d *testDownloader) PlaylistMetadata(_ context.Context, _ string) (ytdl.PlaylistMetadata, error) {
	return ytdl.PlaylistMetadata{}, nil
}

func newTestManager(t *testing.T, db *testDB, fs *testFS, dl *testDownloader) *Manager {
	t.Helper()

	manager, err := NewUpdater(map[string]*feed.Config{}, nil, "http://localhost", dl, db, fs)
	require.NoError(t, err)
	return manager
}

func TestFetchEpisodes_StatusFiltering(t *testing.T) {
	db := &testDB{episodes: map[string][]*model.Episode{
		"feed1": {
			{ID: "1", Status: model.EpisodeNew},
			{ID: "2", Status: model.EpisodeDownloaded},
			{ID: "3", Status: model.EpisodeError},
			{ID: "4", Status: model.EpisodeCleaned},
		},
	}}

	manager := newTestManager(t, db, &testFS{}, nil)

	list, err := manager.fetchEpisodes(context.Background(), &feed.Config{ID: "feed1", PageSize: 50})
	require.NoError(t, err)

	ids := make([]string, 0, len(list))
	for _, episode := range list {
		ids = append(ids, episode.ID)
	}
	require.Equal(t, []string{"1", "3"}, ids, "only new and errored episodes are queued")
}

func TestFetchEpisodes_PageSizeLimit(t *testing.T) {
	var episodes []*model.Episode
	for i := 1; i <= 5; i++ {
		episodes = append(episodes, &model.Episode{ID: fmt.Sprint(i), Status: model.EpisodeNew})
	}
	db := &testDB{episodes: map[string][]*model.Episode{"feed1": episodes}}

	manager := newTestManager(t, db, &testFS{}, nil)

	list, err := manager.fetchEpisodes(context.Background(), &feed.Config{ID: "feed1", PageSize: 3})
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, "1", list[0].ID)
	require.Equal(t, "3", list[2].ID)
}

func TestFetchEpisodes_Filters(t *testing.T) {
	db := &testDB{episodes: map[string][]*model.Episode{
		"feed1": {
			{ID: "1", Title: "keep this one", Status: model.EpisodeNew},
			{ID: "2", Title: "skip that one", Status: model.EpisodeNew},
		},
	}}

	manager := newTestManager(t, db, &testFS{}, nil)

	cfg := &feed.Config{ID: "feed1", PageSize: 50, Filters: feed.Filters{Title: "keep"}}
	list, err := manager.fetchEpisodes(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "1", list[0].ID)
}

func TestDownloadEpisodes_AlreadyOnDisk(t *testing.T) {
	episode := &model.Episode{ID: "1", Status: model.EpisodeNew}
	db := &testDB{episodes: map[string][]*model.Episode{"feed1": {episode}}}
	cfg := &feed.Config{ID: "feed1", PageSize: 50}

	fs := &testFS{files: map[string][]byte{
		"feed1/" + feed.EpisodeName(cfg, episode): []byte("existing content"),
	}}

	dl := &testDownloader{download: func(context.Context, *feed.Config, *model.Episode) (io.ReadCloser, error) {
		t.Fatal("downloader must not be called for episodes already on disk")
		return nil, nil
	}}

	manager := newTestManager(t, db, fs, dl)

	err := manager.downloadEpisodes(context.Background(), cfg, []*model.Episode{episode})
	require.NoError(t, err)
	require.Equal(t, model.EpisodeDownloaded, episode.Status)
	require.Equal(t, int64(len("existing content")), episode.Size)
	require.Empty(t, fs.created)
}

func TestDownloadEpisodes_Success(t *testing.T) {
	episode := &model.Episode{ID: "1", Title: "Test Episode", Status: model.EpisodeNew}
	db := &testDB{episodes: map[string][]*model.Episode{"feed1": {episode}}}
	cfg := &feed.Config{ID: "feed1", PageSize: 50}
	fs := &testFS{}

	dl := &testDownloader{download: func(context.Context, *feed.Config, *model.Episode) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("episode data")), nil
	}}

	manager := newTestManager(t, db, fs, dl)

	err := manager.downloadEpisodes(context.Background(), cfg, []*model.Episode{episode})
	require.NoError(t, err)

	require.Equal(t, model.EpisodeDownloaded, episode.Status)
	require.Equal(t, int64(len("episode data")), episode.Size)
	require.Equal(t, []string{"feed1/" + feed.EpisodeName(cfg, episode)}, fs.created)
}

func TestDownloadEpisodes_PostDownloadHookEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook execution test requires /bin/sh")
	}

	episode := &model.Episode{ID: "1", Title: "Hooked Episode", Status: model.EpisodeNew}
	db := &testDB{episodes: map[string][]*model.Episode{"feed1": {episode}}}

	outFile := filepath.Join(t.TempDir(), "hook.out")
	cfg := &feed.Config{
		ID:       "feed1",
		PageSize: 50,
		PostEpisodeDownload: []*feed.ExecHook{
			{Command: []string{`printf '%s\n%s\n%s\n' "$EPISODE_FILE" "$FEED_NAME" "$EPISODE_TITLE" > ` + outFile}},
		},
	}

	dl := &testDownloader{download: func(context.Context, *feed.Config, *model.Episode) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("data")), nil
	}}

	manager := newTestManager(t, db, &testFS{}, dl)

	err := manager.downloadEpisodes(context.Background(), cfg, []*model.Episode{episode})
	require.NoError(t, err)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Equal(t, []string{"feed1/" + feed.EpisodeName(cfg, episode), "feed1", "Hooked Episode"}, lines)
}

func TestDownloadEpisodes_ErrorSetsStatusAndRunsHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook execution test requires /bin/sh")
	}

	episode := &model.Episode{ID: "1", Title: "Broken Episode", Status: model.EpisodeNew}
	db := &testDB{episodes: map[string][]*model.Episode{"feed1": {episode}}}

	outFile := filepath.Join(t.TempDir(), "hook.out")
	cfg := &feed.Config{
		ID:       "feed1",
		PageSize: 50,
		OnEpisodeDownloadError: []*feed.ExecHook{
			{Command: []string{`printf '%s\n%s\n%s\n' "$FEED_NAME" "$EPISODE_TITLE" "$ERROR_MESSAGE" > ` + outFile}},
		},
	}

	dl := &testDownloader{download: func(context.Context, *feed.Config, *model.Episode) (io.ReadCloser, error) {
		return nil, errors.New("video is unavailable")
	}}

	fs := &testFS{}
	manager := newTestManager(t, db, fs, dl)

	err := manager.downloadEpisodes(context.Background(), cfg, []*model.Episode{episode})
	require.NoError(t, err, "download errors are recorded per episode, not returned")

	require.Equal(t, model.EpisodeError, episode.Status)
	require.Empty(t, fs.created)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Equal(t, []string{"feed1", "Broken Episode", "video is unavailable"}, lines)
}

func TestDownloadEpisodes_TooManyRequestsStopsBatch(t *testing.T) {
	first := &model.Episode{ID: "1", Status: model.EpisodeNew}
	second := &model.Episode{ID: "2", Status: model.EpisodeNew}
	db := &testDB{episodes: map[string][]*model.Episode{"feed1": {first, second}}}
	cfg := &feed.Config{ID: "feed1", PageSize: 50}

	var calls int
	dl := &testDownloader{download: func(context.Context, *feed.Config, *model.Episode) (io.ReadCloser, error) {
		calls++
		return nil, ytdl.ErrTooManyRequests
	}}

	manager := newTestManager(t, db, &testFS{}, dl)

	err := manager.downloadEpisodes(context.Background(), cfg, []*model.Episode{first, second})
	require.NoError(t, err)

	require.Equal(t, 1, calls, "batch must stop after a 429 response")
	require.Equal(t, model.EpisodeNew, first.Status, "429 must not mark the episode as errored")
	require.Equal(t, model.EpisodeNew, second.Status)
}
