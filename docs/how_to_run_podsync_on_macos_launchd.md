# Run Podsync on macOS with launchd

This guide shows how to run Podsync automatically in the background on macOS
using a [launchd](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)
LaunchAgent.

Podsync's headless mode (`--headless`) runs a single round of feed updates and
then exits, so launchd re-runs it on a schedule instead of keeping a long-lived
process alive. If you instead want the resident daemon (HTTP server + built-in
cron scheduler) that stays running and serves feeds over HTTP, omit `--headless`
and use `KeepAlive` — but for most personal setups the scheduled headless model
below is simpler.

A ready-to-edit sample is provided at
[`init/launchd/com.github.mxpv.podsync.plist`](../init/launchd/com.github.mxpv.podsync.plist).

## 1. Prerequisites

Install the runtime dependencies and build the binary:

```bash
brew install yt-dlp ffmpeg go
git clone https://github.com/mxpv/podsync
cd podsync
make          # produces ./bin/podsync
```

Create your `config.toml` (see [config.toml.example](../config.toml.example)) and
choose a **user-writable** `data_dir` — the Docker-oriented `/app/data` is not
writable on macOS, so use something like `~/Library/Application Support/podsync`.

## 2. Install the LaunchAgent

Copy the sample plist into your per-user LaunchAgents directory:

```bash
cp init/launchd/com.github.mxpv.podsync.plist ~/Library/LaunchAgents/
```

Edit `~/Library/LaunchAgents/com.github.mxpv.podsync.plist` and replace every
`/ABSOLUTE/PATH/...` placeholder with real absolute paths (the built binary, your
`config.toml`, and a working directory for logs). launchd does **not** expand
`~`, `$HOME`, or other variables inside these values, so full paths are required.

The sample sets:

- `StartInterval` to `21600` seconds (6 hours), matching Podsync's default
  `update_period`. Adjust to taste.
- `RunAtLoad` to `true`, so an update runs immediately when the agent is loaded.
- A `PATH` environment variable that includes `/opt/homebrew/bin` (Apple Silicon)
  and `/usr/local/bin` (Intel) so launchd can find `yt-dlp` and `ffmpeg`.

## 3. Load and verify

```bash
launchctl load ~/Library/LaunchAgents/com.github.mxpv.podsync.plist
```

On recent macOS you can equivalently use the modern subcommand:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.github.mxpv.podsync.plist
```

Confirm the job is registered and watch the logs:

```bash
launchctl list | grep podsync
tail -f /ABSOLUTE/PATH/TO/podsync-workdir/podsync.log
```

## 4. Manage the service

Trigger an immediate run:

```bash
launchctl start com.github.mxpv.podsync
```

Stop and unload it:

```bash
launchctl unload ~/Library/LaunchAgents/com.github.mxpv.podsync.plist
```

After editing the plist, unload and load it again for changes to take effect.
