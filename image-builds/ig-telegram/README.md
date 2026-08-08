# ig-telegram

A Telegram bot that downloads public Instagram media (posts, reels, profile grids) using [go-rod](https://github.com/go-rod/rod) for browser automation and [Temporal](https://temporal.io) for workflow orchestration and retries.

## How it works

The binary runs a Temporal worker and either a Telegram bot (default) or a one-shot CLI command. When a user sends an Instagram link, the bot starts a Temporal workflow that:

1. Navigates to the Instagram embed page using headless Chromium
2. Extracts direct CDN media URLs (videos and images)
3. Downloads each media item to local disk
4. Sends the file to the configured Telegram chat (if credentials are set)
5. Cleans up the local file after sending

Profile batch mode fetches all post links from a profile and processes each as a child workflow with a delay to avoid rate-limiting.

## Usage

```
ig-telegram                              # run worker + telegram bot (default)
ig-telegram bot                          # same, explicit
ig-telegram post <url>                   # download a single post/reel
ig-telegram profile <username> [limit] [reels]
```

### Bot commands

| Command | Description |
|---------|-------------|
| `<instagram url>` | Download a single post or reel |
| `/profile <username> [limit] [reels]` | Batch download from a profile |

Only the user whose Telegram ID matches `ALLOWED_USERS` can use the bot.

## Configuration

All configuration is via environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `TELEGRAM_BOT_TOKEN` | bot mode | — | Telegram bot API token |
| `TELEGRAM_CHAT_ID` | bot mode | — | Target chat ID for sending media |
| `ALLOWED_USERS` | bot mode | — | Telegram user ID allowed to use the bot |
| `TEMPORAL_ADDRESS` | no | `temporal-frontend:7233` | Temporal frontend address |
| `TEMPORAL_NAMESPACE` | no | `default` | Temporal namespace |
| `TASK_QUEUE` | no | `ig-telegram` | Temporal task queue name |

CLI mode (`post`/`profile`) does not require Telegram credentials. Without them, media is downloaded to the current working directory and file paths are printed.

## Running locally

### With Go

```bash
# Port-forward to the in-cluster Temporal frontend
kubectl port-forward -n temporal svc/temporal-frontend 7233:7233

# Download a single post
TEMPORAL_ADDRESS=localhost:7233 go run . post https://www.instagram.com/reel/ABC123/

# Batch download from a profile (limit 5, reels only)
TEMPORAL_ADDRESS=localhost:7233 go run . profile docteur_zoe_ 5 reels

# Run the bot
TEMPORAL_ADDRESS=localhost:7233 \
TELEGRAM_BOT_TOKEN=<token> \
TELEGRAM_CHAT_ID=<chat_id> \
ALLOWED_USERS=<user_id> \
go run . bot
```

### With Docker

```bash
# Build
docker build -t ig-telegram .

# Download a post, saving media to the current directory
docker run --rm \
  -e TEMPORAL_ADDRESS=host.docker.internal:7233 \
  -v "$PWD:/work" -w /work \
  ig-telegram post https://www.instagram.com/reel/ABC123/

# Run the bot
docker run --rm \
  -e TEMPORAL_ADDRESS=host.docker.internal:7233 \
  -e TELEGRAM_BOT_TOKEN=<token> \
  -e TELEGRAM_CHAT_ID=<chat_id> \
  -e ALLOWED_USERS=<user_id> \
  ig-telegram bot
```

## Credential reuse

The bot reuses the same credential keys as the `tts9000` app (`TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `ALLOWED_USERS`). When deploying to Kubernetes, mount an ExternalSecret pointing to the same OpenBao path (`tts9000/credentials`) to avoid duplicating secrets.

## Architecture

```
ig-telegram/
├── main.go              # entry point: subcommand dispatch, worker setup
├── config.go            # env var loading and validation
├── instagram/
│   ├── types.go         # MediaItem, ProfileFilter
│   └── client.go        # go-rod browser wrapper (embed page scraping, network interception, download)
├── telegram/
│   ├── sender.go        # Telegram Bot API wrapper (media, messages, albums)
│   └── bot.go           # update polling loop, auth guard, command dispatch
├── workflows/
│   ├── activities.go    # Temporal activities (scrape, download, send, cleanup)
│   └── workflows.go     # SinglePostWorkflow, ProfileBatchWorkflow (child workflows + retry policies)
├── Dockerfile           # Alpine + Chromium for go-rod
└── build.yaml           # image build config
```

### Scrape strategy

The scraper tries multiple strategies in order:

1. **Embed page** — loads `https://instagram.com/reel/<id>/embed/`, a lightweight HTML page with direct CDN URLs. Regex extracts `.mp4` and `.jpg` URLs from `fbcdn.net`/`scontent` domains.
2. **Main page with network interception** — if the embed page fails, loads the full Instagram page and intercepts `NetworkResponseReceived` events to capture video/audio CDN URLs that the browser fetches (bypassing `blob:` URLs in the DOM).
3. **DOM extraction** — falls back to Open Graph meta tags, `<video><source>` elements, and JavaScript evaluation of JSON-LD data.

### Retry policy

Browser-scraping activities use Temporal's retry policy:
- Initial interval: 5s
- Backoff coefficient: 2.0
- Maximum attempts: 3
- Maximum interval: 30s

Child workflows in profile batch mode use a separate policy with 10s initial interval and 2 maximum attempts.

## Limitations

- Instagram frequently changes DOM structure. The embed page approach is more resilient than scraping the main page but may still break.
- Profile pages may require login — public post/reel URLs are more reliable.
- Carousel posts may not extract all items from the embed page.
- The bot runs a single browser instance. High concurrency may cause contention on the browser mutex.
