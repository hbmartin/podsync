package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/mxpv/podsync/pkg/enrich"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/metrics"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/services/migrate"
	"github.com/mxpv/podsync/services/update"
	"github.com/mxpv/podsync/services/web"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/ytdl"
)

type Opts struct {
	ConfigPath             string       `long:"config" short:"c" default:"config.toml" env:"PODSYNC_CONFIG_PATH"`
	Headless               bool         `long:"headless"`
	MigrateFilenames       bool         `long:"migrate-filenames" description:"Migrate existing downloaded filenames to current filename_template and exit"`
	MigrateFilenamesDryRun bool         `long:"migrate-filenames-dry-run" description:"Preview filename migration without writing changes (requires --migrate-filenames)"`
	Debug                  bool         `long:"debug"`
	NoBanner               bool         `long:"no-banner"`
	Setup                  SetupCommand `command:"setup" description:"Interactively generate a config.toml"`
}

const banner = `
 _______  _______  ______   _______           _        _______ 
(  ____ )(  ___  )(  __  \ (  ____ \|\     /|( (    /|(  ____ \
| (    )|| (   ) || (  \  )| (    \/( \   / )|  \  ( || (    \/
| (____)|| |   | || |   ) || (_____  \ (_) / |   \ | || |      
|  _____)| |   | || |   | |(_____  )  \   /  | (\ \) || |      
| (      | |   | || |   ) |      ) |   ) (   | | \   || |      
| )      | (___) || (__/  )/\____) |   | |   | )  \  || (____/\
|/       (_______)(______/ \_______)   \_/   |/    )_)(_______/
`

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	arch    = ""
)

type updateRunner interface {
	Update(ctx context.Context, feedConfig *feed.Config) error
}

type serviceServer interface {
	ListenAndServe() error
	ListenAndServeTLS(certFile, keyFile string) error
	Shutdown(ctx context.Context) error
}

type serviceConfig struct {
	Feeds           map[string]*feed.Config
	Manager         updateRunner
	Metrics         *metrics.Metrics
	Server          serviceServer
	ServerAddr      string
	TLS             bool
	CertificatePath string
	KeyFilePath     string
	Stop            <-chan os.Signal
	QueueSize       int
}

func main() {
	log.SetFormatter(&log.TextFormatter{
		TimestampFormat: time.RFC3339,
		FullTimestamp:   true,
	})

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Parse args
	opts := Opts{}
	parser := flags.NewParser(&opts, flags.Default)
	parser.SubcommandsOptional = true
	_, err := parser.Parse()
	if err != nil {
		log.WithError(err).Fatal("failed to parse command line arguments")
	}

	if opts.Debug {
		log.SetLevel(log.DebugLevel)
	}
	if opts.MigrateFilenamesDryRun && !opts.MigrateFilenames {
		log.Fatal("--migrate-filenames-dry-run requires --migrate-filenames")
	}

	// Interactive config generation, runs before the config file is loaded
	if parser.Active != nil && parser.Active.Name == "setup" {
		if err := runSetup(opts.ConfigPath, os.Stdin, os.Stdout); err != nil {
			log.WithError(err).Fatal("setup failed")
		}
		return
	}

	if !opts.NoBanner {
		log.Info(banner)
	}

	log.WithFields(log.Fields{
		"version": version,
		"commit":  commit,
		"date":    date,
		"arch":    arch,
	}).Info("running podsync")

	// Load TOML file
	log.Debugf("loading configuration %q", opts.ConfigPath)
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		log.WithError(err).Fatal("failed to load configuration file")
	}

	if cfg.Log.Filename != "" {
		log.Infof("Using log file: %s", cfg.Log.Filename)

		log.SetOutput(&lumberjack.Logger{
			Filename:   cfg.Log.Filename,
			MaxSize:    cfg.Log.MaxSize,
			MaxBackups: cfg.Log.MaxBackups,
			MaxAge:     cfg.Log.MaxAge,
			Compress:   cfg.Log.Compress,
		})

		// Optionally enable debug mode from config.toml
		if cfg.Log.Debug {
			log.SetLevel(log.DebugLevel)
		}
	}

	database, err := db.NewBadger(&cfg.Database)
	if err != nil {
		log.WithError(err).Fatal("failed to open database")
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.WithError(err).Error("failed to close database")
		}
	}()

	var storage fs.Storage
	switch cfg.Storage.Type {
	case "local":
		storage, err = fs.NewLocal(cfg.Storage.Local.DataDir, cfg.Server.WebUIEnabled, cfg.Server.NoListing)
	case "s3":
		storage, err = fs.NewS3(cfg.Storage.S3) // serving files from S3 is not supported, so no WebUI either
	default:
		log.Fatalf("unknown storage type: %s", cfg.Storage.Type)
	}
	if err != nil {
		log.WithError(err).Fatal("failed to open storage")
	}

	if opts.MigrateFilenames {
		if cfg.Storage.Type == "s3" && !opts.MigrateFilenamesDryRun {
			log.Fatal("--migrate-filenames is not supported with storage.type = \"s3\"; use --migrate-filenames-dry-run or migrate with local storage")
		}

		migration := migrate.New(cfg.Feeds, database, storage, opts.MigrateFilenamesDryRun)
		result, err := migration.Run(ctx)
		if err != nil {
			log.WithError(err).Fatal("filename migration failed")
		}
		log.WithFields(log.Fields{
			"feeds":                   result.Feeds,
			"episodes":                result.Episodes,
			"migrated":                result.Migrated,
			"already_good":            result.AlreadyGood,
			"missing_old":             result.MissingOld,
			"skipped_existing_target": result.SkippedDueToExistingTarget,
			"dry_run":                 opts.MigrateFilenamesDryRun,
		}).Info("filename migration completed")
		return
	}

	downloader, err := ytdl.New(ctx, cfg.Downloader)
	if err != nil {
		log.WithError(err).Fatal("youtube-dl error")
	}

	// Run updater thread
	log.Debug("creating key providers")
	keys := map[model.Provider]feed.KeyProvider{}
	for name, list := range cfg.Tokens {
		provider, err := feed.NewKeyProvider(list)
		if err != nil {
			log.WithError(err).Fatalf("failed to create key provider for %q", name)
		}
		keys[name] = provider
	}

	log.Debug("creating episode enricher")
	enricher := enrich.New(cfg.Tools)

	// Metrics collection is opt-in; when disabled the collector is nil and all
	// recording calls are cheap no-ops.
	var metricsCollector *metrics.Metrics
	if cfg.Server.Metrics {
		log.Debug("creating metrics collector")
		metricsCollector = metrics.New()
	}

	log.Debug("creating update manager")
	manager, err := update.NewUpdater(cfg.Feeds, keys, cfg.Server.Hostname, downloader, enricher, database, storage, metricsCollector)
	if err != nil {
		log.WithError(err).Fatal("failed to create updater")
	}

	// In Headless mode, do one round of feed updates and quit
	if opts.Headless {
		for _, _feed := range cfg.Feeds {
			if err := manager.Update(ctx, _feed); err != nil {
				log.WithError(err).Errorf("failed to update feed: %s", _feed.URL)
			}
		}
		return
	}

	var srv serviceServer
	var srvAddr string
	if cfg.Storage.Type != "s3" {
		// Run web server. S3 content is hosted externally, so there is no
		// local media server in that mode.
		webServer := web.New(cfg.Server, storage, database, metricsCollector)
		srv = webServer
		srvAddr = webServer.Addr
	}

	if err := runService(ctx, serviceConfig{
		Feeds:           cfg.Feeds,
		Manager:         manager,
		Metrics:         metricsCollector,
		Server:          srv,
		ServerAddr:      srvAddr,
		TLS:             cfg.Server.TLS,
		CertificatePath: cfg.Server.CertificatePath,
		KeyFilePath:     cfg.Server.KeyFilePath,
		Stop:            stop,
	}); err != nil && (err != context.Canceled && err != http.ErrServerClosed) {
		log.WithError(err).Error("wait error")
	}
	log.Info("gracefully stopped")
}

func runService(parent context.Context, cfg serviceConfig) error {
	if cfg.Manager == nil {
		return fmt.Errorf("update manager is required")
	}

	queueSize := cfg.QueueSize
	if queueSize == 0 {
		queueSize = 16
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	updates := make(chan *feed.Config, queueSize)
	group, ctx := errgroup.WithContext(ctx)

	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)))
	entries := make(map[string]cron.EntryID)
	var entriesMu sync.RWMutex

	nextUpdate := func(feedID string) time.Time {
		entriesMu.RLock()
		entryID := entries[feedID]
		entriesMu.RUnlock()
		if entryID == 0 {
			return time.Time{}
		}
		return c.Entry(entryID).Next
	}

	enqueue := func(feedConfig *feed.Config) {
		select {
		case updates <- feedConfig:
			cfg.Metrics.SetQueueDepth(len(updates))
		case <-ctx.Done():
		}
	}

	group.Go(func() error {
		for {
			select {
			case feedConfig := <-updates:
				cfg.Metrics.SetQueueDepth(len(updates))
				if err := cfg.Manager.Update(ctx, feedConfig); err != nil {
					log.WithError(err).Errorf("failed to update feed: %s", feedConfig.URL)
				} else if next := nextUpdate(feedConfig.ID); !next.IsZero() {
					log.Infof("next update of %s: %s", feedConfig.ID, next)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	group.Go(func() error {
		var cronID cron.EntryID

		for _, feedConfig := range cfg.Feeds {
			// Track if this feed has an explicit cron schedule.
			hasExplicitCronSchedule := feedConfig.CronSchedule != ""

			if feedConfig.CronSchedule == "" {
				feedConfig.CronSchedule = fmt.Sprintf("@every %s", feedConfig.UpdatePeriod.String())
			}
			cronFeed := feedConfig
			var err error
			if cronID, err = c.AddFunc(cronFeed.CronSchedule, func() {
				log.Debugf("adding %q to update queue", cronFeed.ID)
				enqueue(cronFeed)
			}); err != nil {
				return fmt.Errorf("can't create cron task for feed %s: %w", cronFeed.ID, err)
			}

			entriesMu.Lock()
			entries[cronFeed.ID] = cronID
			entriesMu.Unlock()
			log.Debugf("-> %s (update '%s')", cronFeed.ID, cronFeed.CronSchedule)

			// Only perform initial update if no explicit cron schedule is configured.
			// This prevents unwanted updates when using fixed schedules in Docker deployments.
			if !hasExplicitCronSchedule {
				enqueue(cronFeed)
			}
		}

		c.Start()

		<-ctx.Done()

		log.Info("shutting down cron")
		<-c.Stop().Done()

		return ctx.Err()
	})

	if cfg.Server != nil {
		group.Go(func() error {
			log.Infof("running listener at %s", cfg.ServerAddr)
			if cfg.TLS {
				return cfg.Server.ListenAndServeTLS(cfg.CertificatePath, cfg.KeyFilePath)
			}
			return cfg.Server.ListenAndServe()
		})
	}

	group.Go(func() error {
		defer func() {
			if cfg.Server == nil {
				return
			}

			ctxShutDown, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()

			log.Info("shutting down web server")
			if err := cfg.Server.Shutdown(ctxShutDown); err != nil {
				log.WithError(err).Error("server shutdown failed")
			}
		}()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-cfg.Stop:
			cancel()
			return nil
		}
	})

	return group.Wait()
}
