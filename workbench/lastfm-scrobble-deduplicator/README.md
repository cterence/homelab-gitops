# lastfm-scrobble-deduplicator

Detects and removes duplicate scrobbles from a Last.fm profile. It walks
the user's library pages through a browser (chromedp), compares successive
scrobbles of the same track, and deletes the duplicates it finds.

Migrated from https://github.com/cterence/lastfm-scrobble-deduplicator.

## How it works

1. **Login** — loads a saved session cookie from the data dir when possible,
   otherwise logs in to Last.fm with the configured credentials via browser
   automation and saves the cookie for reuse.
2. **Scan** — navigates the library within the optional `--from` / `--to`
   range, extracts scrobbles from each page.
3. **Detect** — two successive scrobbles of the same track are duplicates
   when the time between them is less than `--duplicate-threshold` percent
   of the track's duration. Track durations come from MusicBrainz, with a
   local file cache to avoid repeated API queries.
4. **Delete** — when `--delete` is enabled, duplicates are removed through
   the Last.fm web UI. By default the tool is dry-run.

Deleted scrobbles are exported to a CSV in the data dir; unknown track
durations are written to `track-durations.yaml` for manual completion.
Runs can optionally report statistics via Telegram.

## Configuration

All flags can be set via environment variables or a YAML config file
(`--config`, default `config.yaml`):

```yaml
cacheType: file          # inmemory | file | redis
lastfm:
  username: your_username
  password: your_password
delete: false            # set true to actually delete scrobbles
duplicateThreshold: 90   # percent of track duration
from: yesterday          # dd-mm-yyyy, or yesterday/today
dataDir: ./data
```

Environment variables map to flags: `LASTFM_USERNAME`, `LASTFM_PASSWORD`,
`DELETE`, `DUPLICATE_THRESHOLD`, `COMPLETE_THRESHOLD`, `START_PAGE`,
`FROM`, `TO`, `CACHE_TYPE`, `BROWSER_URL`, `REDIS_URL`, `DATA_DIR`,
`LOG_LEVEL`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`.

## Browser

The tool needs a Chrome/Chromium instance, either a local one (headless by
default, `--browser-headful` for visible) or a remote one via `--browser-url`
(e.g. a browserless chromium endpoint: `ws://chromium:3000?token=local`).
The in-cluster deployment uses a browserless chromium sidecar.

## Development

```bash
go build -o lastfm-scrobble-deduplicator .
go test ./...
```

Delete the built binary afterwards.

## Deployment

Consumed by `k8s-apps/lastfm-scrobble-deduplicator` as a daily CronJob,
image `registry.terence.cloud/lastfm-scrobble-deduplicator:<tag>` matching
`build.yaml`. Bump both tags together on code changes.
