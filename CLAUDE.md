# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Podsync is a Go-based service that converts YouTube, Vimeo, SoundCloud, and Twitch channels into podcast feeds. It downloads video/audio content and generates RSS feeds that can be consumed by podcast clients.

## Key Architecture Components

### Main Application (`cmd/podsync/`)
- **main.go**: Entry point with CLI argument parsing, signal handling, and service orchestration
- **config.go**: TOML configuration loading and validation with defaults

### Core Packages (`pkg/`)
- **builder/**: Media downloaders for different platforms (YouTube, Vimeo, SoundCloud, Twitch)
- **feed/**: RSS/podcast feed generation and management, OPML export, hooks, API key rotation
- **db/**: BadgerDB-based storage for metadata and state
- **fs/**: Storage abstraction supporting local filesystem and S3-compatible storage
- **model/**: Core data structures and domain models
- **ytdl/**: YouTube-dl wrapper for media downloading

### Services (`services/`)
- **update/**: Feed update orchestration, scheduling, episode filtering via matcher.go
- **web/**: HTTP server for serving podcast feeds, media files, and health checks
- **migrate/**: Filename migration tooling for transitioning to custom filename templates

### Key Dependencies
- youtube-dl/yt-dlp for media downloading
- BadgerDB for local storage
- go-toml for configuration
- robfig/cron for scheduling
- AWS SDK for S3 storage

## Episode Lifecycle

Understanding how episodes flow through the system:

### Discovery Phase
- Episodes are discovered during feed updates via platform APIs (YouTube Data API v3, Vimeo API, SoundCloud, Twitch)
- `updateFeed()` in `services/update/updater.go:99-154` queries the platform API
- New episodes are stored in BadgerDB with status `EpisodeNew`
- Episodes matching feed URL are identified by provider-specific parsing in `pkg/builder/`

### Download Phase
- `fetchEpisodes()` iterates episodes with status `EpisodeNew` or `EpisodeError`
- Episodes are filtered by match rules (title, description, duration, age) in `services/update/matcher.go:27-72`
- Only `page_size` episodes are queued per update cycle (default 50)
- Downloads happen to temp directory first, then copied to storage to prevent incomplete files
- yt-dlp also fetches subtitles (`--write-subs --write-auto-subs --convert-subs vtt`), the `.info.json` metadata sidecar, and embeds standard tags + thumbnail (`--embed-metadata --embed-thumbnail`); `ytdl.Download()` returns a `DownloadResult` (media path + discovered sidecars), NOT an `io.ReadCloser`
- On success: status set to `EpisodeDownloaded` with file size recorded
- On failure: status set to `EpisodeError`, retry attempted next cycle

### Enrichment Phase (transcripts + chapters)
- Runs after a successful download, BEFORE the media file is copied to storage (chapter embedding mutates the file in place); see `pkg/enrich`
- **Best-effort and attempted exactly once**: failures are logged and the episode is published without the missing pieces; there is NO retry of enrichment, and NO backfill of episodes downloaded before the feature existed
- Transcript chain: platform subtitles (uploaded preferred, auto-captions fallback, language list from `transcripts.languages` → `custom.lang` → `en`) → ordered STT chain (`[[transcripts.stt]]`: `openai` (OpenAI-compatible API), `whisper_cpp`, `command`); output is `<base>.vtt` + `<base>.transcript.json` (PodcastIndex JSON, via `transcript2json` tool or built-in converter in `pkg/enrich/vtt.go`)
- Chapter chain (cheapest first): platform chapters from `.info.json` → description timestamp lists (`podcast-chapters` tool or built-in parser in `pkg/enrich/chapters.go`) → LLM generation via `video-to-chapters-with-transcript` (runs automatically when AssemblyAI + Gemini keys are configured)
- Chapter images: one JPEG frame per chapter (chapter start + 1s, max width `image_max_width`, default 1280) extracted with ffmpeg into `<base>.chapter-NN.jpg`; for audio feeds a temporary low-res video is fetched lazily (at most once per episode) unless `chapters.fetch_video_for_audio = false`
- Chapter embedding: MP3 gets ID3v2 CHAP/CTOC frames (`pkg/media/id3.go`, bogem/id3v2); MP4 gets container chapters (yt-dlp `--embed-chapters` for platform chapters, ffmpeg remux for generated ones)
- Sidecar metadata is persisted on `Episode.Enrichment` (file names only); `buildXML` emits tags from it without probing storage
- Helper tools are OPTIONAL external binaries (`[tools]` config, resolved from PATH); missing tools log a warning and fall back to built-in Go implementations (or skip the feature)

### Cleanup Phase (Important!)
- `cleanup()` in `updater.go:373-441` runs AFTER each successful update cycle
- **Only triggered if `clean.keep_last` is configured** (global or per-feed)
- Keeps most recent N episodes by PubDate (descending order)
- Deleted episodes have status changed to `EpisodeCleaned` and title/description cleared
- Transcript/chapter sidecar files are deleted together with the media file, and `Episode.Enrichment` is set to nil
- **Files are deleted from storage but database records are retained**
- This is a soft-delete: episodes remain in the database forever

### Episode Removal from Database
- Episodes are removed from the database only if they:
  1. Are no longer returned by the platform API, AND
  2. Have status `EpisodeNew` (never downloaded)
- Episodes with status `EpisodeDownloaded` or `EpisodeCleaned` are NEVER removed from the database
- There is NO mechanism to compact or prune the database of old episode records

## Database Behavior (BadgerDB)

### Data Storage
- Uses versioned keyspace: `podsync/v1/`
- Key prefixes: `feed/{feedID}` for feeds, `episode/{feedID}/{episodeID}` for episodes
- Both use JSON serialization
- Records are append-only; deleted episodes remain with `EpisodeCleaned` status

### Database Growth and Limitations
- **Database grows indefinitely** as new episodes are discovered
- Deleted/cleaned episodes remain in DB forever (not physically removed)
- **No built-in compaction or garbage collection** mechanism
- No configuration option to prune old episode records
- `keep_last` only deletes files, not database records
- For very large feeds, database file size can grow significantly over time

### Configuration Options
```toml
[database]
dir = "/path/to/db"  # defaults to {config_dir}/db

[database.badger]
truncate = true      # enable value log truncation
file_io = true       # use file I/O instead of mmap
```

## Configuration Reference

### Feed Configuration (`[feeds.{ID}]`)
```toml
[feeds.my_feed]
url = "https://youtube.com/..."        # Required: platform URL
page_size = 50                         # Episodes to fetch per update (default 50)
update_period = "6h"                   # How often to check (default 6h)
cron_schedule = "0 */6 * * *"          # Cron expression (overrides update_period)
quality = "high"                       # "high" or "low" (default "high")
format = "video"                       # "audio", "video", or "custom"
max_height = 720                       # Maximum video height (720, 1080, 1440, etc.)
playlist_sort = "desc"                 # "asc" or "desc" for playlist ordering
filename_template = "{{id}}"           # Tokens: {{id}}, {{title}}, {{pub_date}}, {{feed_id}}
opml = true                            # Include in OPML export
private_feed = false                   # Don't index by podcast aggregators (all providers)
youtube_dl_args = ["--arg1", "val"]    # Additional youtube-dl arguments

[feeds.my_feed.custom_format]          # When format = "custom"
youtube_dl_format = "bestvideo+bestaudio"
extension = "mkv"

[feeds.my_feed.clean]
keep_last = 10                         # Keep only N most recent episodes (deletes files, not DB records)

[feeds.my_feed.filters]
title = "regex pattern"                # Include if title matches
not_title = "regex pattern"            # Exclude if title matches
description = "regex pattern"          # Include if description matches
not_description = "regex pattern"      # Exclude if description matches
min_duration = 60                      # Minimum duration in seconds
max_duration = 3600                    # Maximum duration in seconds
min_age = 1                            # Skip episodes newer than N days
max_age = 365                          # Skip episodes older than N days

[feeds.my_feed.transcripts]            # Per-feed override of global [transcripts]
enabled = true                         # Default true
languages = ["en"]                     # Default: custom.lang, then "en"
# [[feeds.my_feed.transcripts.stt]] entries replace the global chain wholesale

[feeds.my_feed.chapters]               # Per-feed override of global [chapters]
enabled = true                         # Default true
extract_images = true                  # Frame per chapter as chapter art (default true)
fetch_video_for_audio = true           # Temp low-res video for audio feeds (default true)
image_max_width = 1280                 # Max chapter image width in pixels
```

### Filter Examples

All filters are evaluated with AND logic — an episode must satisfy every configured filter to be downloaded. Use [Go regular expression syntax](https://pkg.go.dev/regexp/syntax) to express complex conditions within a single filter field.

**Exclude episodes with any of several keywords** (OR logic via regex alternation):
```toml
[feeds.my_feed.filters]
# Exclude episodes whose title contains "Live", "LIVE", "Q&A", or "q&a"
not_title = "(?i)(live|q&a)"
```

**Include only episodes matching any of several keywords**:
```toml
[feeds.my_feed.filters]
# Include only episodes whose title contains "tutorial", "how-to", or "guide" (case-insensitive)
title = "(?i)(tutorial|how.to|guide)"
```

**Combine title and duration filters** (both conditions must be satisfied):
```toml
[feeds.my_feed.filters]
# Exclude short clips and previews AND require a minimum duration of 10 minutes
not_title = "(?i)(short clip|preview|trailer)"
min_duration = 600
```

**Match a phrase** (use `\b` for word boundaries or anchor patterns with `^`/`$`):
```toml
[feeds.my_feed.filters]
# Include only episodes that contain the exact phrase "full episode" (case-insensitive)
title = "(?i)full episode"
```

> **Note**: `title` and `description` filters include episodes that match; `not_title` and `not_description` exclude episodes that match. Duration and age filters always exclude episodes outside the specified range.

```toml
[feeds.my_feed.custom]                 # Override feed metadata
locked = true                          # podcast:locked tag; default: locked iff ownerEmail set
title = "Custom Title"
description = "Custom description"
author = "Author Name"
link = "https://example.com"
cover_art = "https://example.com/image.jpg"
cover_art_quality = "high"             # "high" or "low"
category = "Technology"
subcategories = ["Software How-To"]
explicit = false
lang = "en"
ownerName = "Owner"
ownerEmail = "owner@example.com"
```

### Hooks Configuration
```toml
[feeds.my_feed.hooks.on_episode_download]
command = ["notify-send", "Downloaded: ${EPISODE_TITLE}"]
timeout = 60  # seconds

[feeds.my_feed.hooks.on_episode_download_error]
command = ["logger", "Failed: ${ERROR_MESSAGE}"]
timeout = 60
```
Environment variables available: `EPISODE_FILE`, `FEED_NAME`, `EPISODE_TITLE`, `ERROR_MESSAGE`

### Server Configuration
```toml
[server]
port = 8080                            # HTTP port
hostname = "https://example.com"       # External URL for feed links
bind_address = "*"                     # IP to bind ("*" for all)
path = "feeds"                         # URL path prefix (alphanumeric only)
web_ui = false                         # Enable web UI
tls = false                            # Enable HTTPS
certificate_path = "/path/to/cert.pem"
key_file_path = "/path/to/key.pem"
debug_endpoints = false                # Enable /debug/vars metrics
no_index = false                       # Block search engine indexing (serves robots.txt and X-Robots-Tag header)
no_listing = false                     # Disable directory listings, return 404 for folder access
```

### Storage Configuration
```toml
[storage]
type = "local"                         # "local" or "s3"

[storage.local]
data_dir = "/path/to/data"             # Required for local storage

[storage.s3]
endpoint_url = "https://s3.amazonaws.com"
region = "us-east-1"
bucket = "my-bucket"
prefix = "podsync"
```

### API Tokens
```toml
[tokens]
youtube = "API_KEY"                    # Single key
youtube = ["KEY1", "KEY2", "KEY3"]     # Multiple keys for rotation
vimeo = "TOKEN"
soundcloud = "KEY"
twitch = "CLIENT_ID:CLIENT_SECRET"     # Must include both
```
Environment variables: `PODSYNC_YOUTUBE_API_KEY`, `PODSYNC_VIMEO_API_KEY`, etc. (space-separated for multiple keys)

### Downloader Configuration
```toml
[downloader]
self_update = false                    # Auto-update youtube-dl every 24h
timeout = 10                           # Download timeout in minutes (default 10)
custom_binary = "/path/to/yt-dlp"      # Custom youtube-dl/yt-dlp binary
```

### Global Cleanup
```toml
[cleanup]
keep_last = 50                         # Applied to all feeds unless overridden
```

### Transcripts, Chapters and Helper Tools (global)
```toml
[tools]                                # Optional external helpers, resolved from PATH
transcript2json   = "transcript2json"                    # hbmartin/podcast-transcript-convert
podcast_chapters  = "podcast-chapters"                   # hbmartin/podcast-chapter-tools
video_to_chapters = "video-to-chapters-with-transcript"  # hbmartin/video-to-chapters-with-transcript
ffmpeg            = "ffmpeg"

[transcripts]                          # Global defaults, feeds inherit unless they have their own section
enabled = true
languages = ["en"]

[[transcripts.stt]]                    # Ordered STT fallback chain (no subs at all); empty = disabled
type = "openai"                        # OpenAI-compatible /v1/audio/transcriptions
base_url = "https://api.openai.com/v1" # Also covers Groq / local whisper servers
api_key = ""                           # Or PODSYNC_STT_API_KEY (fills empty openai keys)
model = "whisper-1"

[[transcripts.stt]]
type = "whisper_cpp"                   # Local whisper.cpp binary (offline)
binary = "whisper-cli"
model_path = "/models/ggml-base.en.bin"

[[transcripts.stt]]
type = "command"                       # Arbitrary command hook
command = ["/opt/my-stt.sh"]           # Env: PODSYNC_AUDIO_FILE, PODSYNC_TRANSCRIPT_OUTPUT, PODSYNC_LANGUAGE
timeout = 1800                         # Seconds (default 1800)

[chapters]
enabled = true
extract_images = true
fetch_video_for_audio = true
image_max_width = 1280

[chapters.llm]                         # AI chapter generation; activates when BOTH keys set
assemblyai_api_key = ""                # Or PODSYNC_ASSEMBLYAI_API_KEY
gemini_api_key = ""                    # Or PODSYNC_GEMINI_API_KEY
```
Merging: a feed without its own `[feeds.X.transcripts]`/`[feeds.X.chapters]` section inherits the global section (by pointer, in `applyDefaults`); a feed WITH its own section does NOT inherit any global values (including the STT chain and LLM keys).

### Logging
```toml
[log]
filename = "/path/to/podsync.log"
max_size = 100                         # MB
max_backups = 3
max_age = 28                           # days
compress = true
debug = false
```

## Platform-Specific Behaviors

All builders share feed construction via `pkg/builder/common.go` (`newFeed`/`addEpisode`), so cross-provider config fields (`private_feed`, `custom.cover_art_quality`, `playlist_sort`) are stored on every feed. Note: `custom.cover_art_quality` and `playlist_sort` currently only affect YouTube's behavior; `private_feed` emits `<itunes:block>` for all providers.

### YouTube (`pkg/builder/youtube.go`)
- **Supported**: Channels, Users, Handles (@username), Playlists
- **Not Supported**: Live streams and Premiered videos (automatically skipped)
- API costs: Channel/User 5 units, Handle ~6 units on first lookup (`channels.list` `forHandle`, then handle→channel ID is cached in memory for the process lifetime, so 5 units thereafter), Playlist 3 units/request
- Thumbnail quality: uses maxres > high > medium > default
- Size estimation based on duration and quality (not actual file size)
- Supports playlist_sort for ordering; `playlist_sort = "desc"` inherently walks the entire playlist (3 units per 50-item page) because the YouTube API has no reverse pagination — memory is bounded, but API cost grows with playlist length

### Vimeo (`pkg/builder/vimeo.go`)
- **Supported**: Channels, Groups, Users
- Paginated API with 50 items per page
- Size estimated from duration and resolution

### SoundCloud (`pkg/builder/soundcloud.go`)
- **Supported**: Playlists only (`/sets/` URLs)
- **Not Supported**: Individual tracks, user profiles, likes playlists
- No API key required
- Size roughly estimated from duration

### Twitch (`pkg/builder/twitch.go`)
- **Supported**: User channels (video archives only)
- **Not Supported**: Clips, highlights, ongoing streams
- Requires `CLIENT_ID:CLIENT_SECRET` token format
- Max 100 videos per request

### API Key Rotation
- All platforms support multiple keys for rotation
- Round-robin rotation via `RotatedKeyProvider` in `pkg/feed/key.go`
- Helps avoid hitting single API quota limits

## Web Server Endpoints

- `/{path}/{feed_id}.xml` - RSS/Podcast feed
- `/{path}/{feed_id}/{episode_name}` - Episode file download
- `/{path}/podsync.opml` - OPML export (feeds with `opml = true`)
- `/{path}/index.html` - Web UI (if enabled, local storage only)
- `/health` - Health check (returns 503 if episodes failed in last 24h)
- `/debug/vars` - Runtime metrics (if `debug_endpoints = true`)
- `/robots.txt` - Search engine blocking (if `no_index = true`)

## Storage Behavior

### Local Storage
- Files stored in `{data_dir}/{feed_id}/{episode_name}`
- Web UI served from `./html/index.html` if enabled

### S3 Storage
- Files stored with key: `{prefix}/{feed_id}/{episode_name}`
- **Cannot serve files via Podsync** - content must be hosted externally
- **Filename migration not supported** (except dry-run)
- Web UI not available

### Filename Generation
- Template tokens: `{{id}}`, `{{title}}`, `{{pub_date}}` (YYYY-MM-DD), `{{feed_id}}`
- Default template: `{{id}}` if not configured
- Sanitization removes invalid characters, normalizes whitespace
- `--migrate-filenames` CLI flag renames existing files to match current template (does NOT rename enrichment sidecar files)

### Sidecar Files (transcripts, chapters, images)
All sidecars are flat files next to the media, named from the episode base name (`pkg/enrich/naming.go`):

| Artifact | Name | Feed tag |
|---|---|---|
| WebVTT transcript | `<base>.vtt` | `<podcast:transcript type="text/vtt">` |
| PodcastIndex transcript | `<base>.transcript.json` | `<podcast:transcript type="application/json">` |
| PodcastIndex chapters | `<base>.chapters.json` | `<podcast:chapters type="application/json+chapters">` |
| Chapter images | `<base>.chapter-NN.jpg` (1-based, zero-padded) | Referenced via `img` inside the chapters JSON |

The web server serves them like any other file (`.vtt` MIME type registered in `services/web/server.go`). Image URLs inside chapters JSON are absolute and baked at download time — a hostname change leaves them stale.

## RSS Generation and Podcasting 2.0 (pkg/rss)

`pkg/rss` is an in-tree fork of `eduncan911/podcast` v1.4.2 (MIT, LICENSE kept) — the upstream library's closed structs cannot emit `podcast:*` namespace tags. A parity test (`pkg/rss/rss_test.go` `TestUpstreamParity`) guarantees output is byte-identical to upstream except for the added `xmlns:podcast` declaration; keep it passing when touching the fork.

Emitted Podcasting 2.0 tags (`pkg/feed/xml.go` `Build`):
- Channel: `podcast:guid` (spec UUIDv5 of `{hostname}/{feedID}.xml`, test vectors in `TestGUID`), `podcast:medium` (`video`/`podcast`), `podcast:locked` (see `custom.locked`), `podcast:person` (channel author as host)
- Item: `podcast:transcript` (VTT + JSON), `podcast:chapters`, `podcast:socialInteract` (original video URL; protocol slug = provider name, which is NOT in the spec's protocol list — pragmatic choice, validators may warn), plus `itunes:isClosedCaptioned` when a transcript exists

## Error Handling

- YouTube 429 (rate limit): stops current batch, retries next cycle
- Download failures: episode status set to `EpisodeError`, retried next cycle
- API failures: logged, scheduler continues with other feeds
- Download timeout: configurable via `downloader.timeout` (default 10 minutes)
- Hooks available for error notification (`on_episode_download_error`)

## Limitations and Known Issues

### Database
- No automatic compaction/garbage collection
- Deleted episodes remain in database forever
- `keep_last` only deletes files, not database records
- Database grows indefinitely with feed updates

### Platforms
- YouTube: Live/Premiered videos skipped automatically
- SoundCloud: Only playlist URLs supported
- Twitch: Archives only, no clips/highlights

### Storage
- S3: Cannot serve files through Podsync, no filename migration
- Local: Web UI requires `./html/index.html` to exist

### Transcripts and Chapters
- Enrichment runs exactly once per episode (no retry on failure) and only for NEW downloads — no backfill for episodes downloaded before the feature existed
- Chapter images inside `<base>.chapters.json` use absolute URLs baked at download time; changing `server.hostname` leaves them stale (feed tags themselves are rebuilt with the new hostname)
- `--embed-chapters`/ffmpeg cannot write ID3 CHAP frames — MP3 embedding is done natively via bogem/id3v2; custom-format containers other than mp3/mp4/m4a/m4v/mov get no in-file chapters
- The exact CLI contracts of the three optional helper tools are best-effort (graceful fallback to built-in implementations); `video-to-chapters-with-transcript` requires AssemblyAI + Gemini keys and a local video file
- STT chain and LLM chapter generation spend API money per episode — they only activate when explicitly configured (keys present)

### Performance
- Feed updates are sequential (not parallel)
- Large playlists paginated with 50-item batches
- All episodes loaded into memory when walking database

## Common Development Commands

### Building
```bash
make build          # Build binary to bin/podsync
make                # Build and run tests
```

### Testing
```bash
make test           # Run all unit tests
go test -v ./...    # Run tests with verbose output
go test ./pkg/...   # Test specific packages
```

### Linting and Formatting
```bash
golangci-lint run   # Run all configured linters and formatters
gofmt -s -w .       # Format all Go files
goimports -w .      # Organize imports and format
```

### Running
```bash
./bin/podsync setup                     # Interactively generate config.toml and exit
./bin/podsync --config config.toml      # Run with config file
./bin/podsync --debug                   # Run with debug logging
./bin/podsync --headless                # Run once and exit (no web server)
./bin/podsync --no-banner               # Suppress ASCII banner on startup
./bin/podsync --migrate-filenames       # Migrate files to current filename_template and exit
./bin/podsync --migrate-filenames-dry-run --migrate-filenames  # Preview migration without changes
```

### Interactive Setup (`cmd/podsync/setup.go`)
`podsync setup` walks new users through generating a `config.toml`:
- Prompts for feed URL(s) (validated with `builder.ParseURL`, re-prompts on invalid input), feed ID, format (video/audio), and quality (high/low), looping for multiple feeds
- Prompts for storage directory, HTTP port, and hostname
- Prompts for a YouTube API key only when a YouTube feed was added (can be left empty and set later via `PODSYNC_YOUTUBE_API_KEY`); Vimeo/Twitch feeds get commented `[tokens]` placeholders with doc links
- Writes to the path given by `-c/--config` (default `config.toml`) with mode `0600` (the file may contain an API key); refuses to overwrite an existing file without confirmation
- The generated TOML is self-checked by parsing it back before writing
- All other options use defaults documented in `config.toml.example` (referenced in the generated file's header)

### Docker
```bash
make docker                           # Build local Docker image
docker run -it --rm localhost/podsync:latest
```

### Development Debugging
Use VS Code with the Go extension. The repository includes `.vscode/launch.json` with a "Debug Podsync" configuration that runs with `config.toml`.

### Dev Container
The repository includes a `.devcontainer/` configuration for VS Code or GitHub Codespaces with Go tooling pre-configured.

## Development Guidelines

### Code Quality
- Write clean, idiomatic Go code following Go conventions and best practices
- Use structured logging with logrus for consistent log formatting
- Ensure proper error handling and meaningful error messages
- Follow the existing code style and patterns in the repository

### Testing and Quality Assurance
- **CRITICAL**: Always run ALL of the following commands before making a commit or opening a PR:
  1. `go fmt ./...` - Format all Go files
  2. `golangci-lint run` - Run all configured linters and formatters
  3. `make test` - Run all unit tests
- Run tests first with `make test` to ensure functionality works correctly
- Run linter with `golangci-lint run` to ensure proper formatting and code quality
- Ensure ALL tests pass AND ALL linting checks pass before committing
- Review code carefully for spelling errors, typos, and grammatical mistakes
- Test changes locally with different configurations when applicable
- The project uses golangci-lint with strict formatting rules - code must pass ALL checks

## Key Conventions

- Configuration validation happens at startup
- Graceful shutdown with context cancellation
- Storage abstraction allows switching between local/S3
- API key rotation support for rate limiting
- Cron-based scheduling for feed updates
- Episode filtering and cleanup capabilities
- Customizable filename templates with migration tooling for existing files

## Maintaining This Documentation

**IMPORTANT**: Keep this CLAUDE.md file up to date whenever making changes to the codebase:

- **New features**: Document new configuration options, CLI flags, endpoints, or capabilities
- **Behavior changes**: Update relevant sections when modifying how episodes are processed, stored, or cleaned up
- **New platform support**: Add platform-specific documentation under "Platform-Specific Behaviors"
- **API changes**: Update configuration examples and available options
- **Bug fixes that affect documented behavior**: Correct any documentation that no longer reflects reality
- **New limitations or removed limitations**: Update the "Limitations and Known Issues" section

This ensures Claude can accurately answer questions about current Podsync behavior and capabilities.

## Formatting and Linting Requirements

This project uses golangci-lint with strict formatting rules configured in `.golangci.yml`. Common formatting requirements include:

- Proper spacing around operators (`if condition {` not `if(condition){`)
- Correct struct field alignment and spacing
- Proper import ordering (standard library, third-party, local packages)
- No trailing whitespace
- Consistent spacing around assignment operators (`key: value` not `key:value`)
- Space after commas in function parameters and struct literals

**Always run `go fmt ./...`, `golangci-lint run`, AND `make test` after making ANY code changes to ensure both functionality and formatting are correct before committing.**

## Key File References

- Main entry: `cmd/podsync/main.go`
- Interactive setup: `cmd/podsync/setup.go`
- Config loading: `cmd/podsync/config.go`
- Feed update: `services/update/updater.go` (episode lifecycle, cleanup)
- Episode filtering: `services/update/matcher.go`
- Database: `pkg/db/badger.go`
- Storage: `pkg/fs/local.go`, `pkg/fs/s3.go`
- Feed generation: `pkg/feed/xml.go` (RSS, filename handling)
- Filename migration: `services/migrate/migrate.go`
- Web server: `services/web/server.go`
- YouTube builder: `pkg/builder/youtube.go`
- Shared builder helpers: `pkg/builder/common.go` (feed construction, episode page-size cutoff)
- Vimeo builder: `pkg/builder/vimeo.go`
- SoundCloud builder: `pkg/builder/soundcloud.go`
- Twitch builder: `pkg/builder/twitch.go`
- URL parsing: `pkg/builder/url.go`
- youtube-dl wrapper: `pkg/ytdl/ytdl.go` (Download/FetchVideo/buildArgs), `pkg/ytdl/download_result.go` (DownloadResult, sidecar discovery)
- Hooks: `pkg/feed/hooks.go`
- API key rotation: `pkg/feed/key.go`
- RSS library fork (iTunes + Podcasting 2.0): `pkg/rss/` (`pc20.go` for podcast: namespace types)
- Enrichment orchestrator: `pkg/enrich/enrich.go` (transcript/chapter chains), `pkg/enrich/naming.go` (sidecar names)
- Built-in converters/parsers: `pkg/enrich/vtt.go` (VTT→PodcastIndex JSON), `pkg/enrich/chapters.go` (info.json/description/flexible chapter parsing)
- STT fallback chain: `pkg/enrich/stt/` (openai.go, whispercpp.go, command.go)
- Media manipulation: `pkg/media/id3.go` (ID3 CHAP/CTOC), `pkg/media/ffmpeg.go` (frames, MP4 chapters)
- Enrichment config structs: `pkg/feed/enrich_config.go`
