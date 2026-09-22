# tts9000

Telegram bot that converts articles to audio using Mistral's Voxtral TTS,
written in Go. Send it a URL and it replies with the article as an MP3 voice
message.

## How it works

1. **Extract** — fetches the URL and pulls the article text with
   `golang.org/x/net/html` (handles Cloudflare/403 style blocks with a
   clear error).
2. **Clean** — sends the raw text to Mistral chat (`mistral-medium-latest`)
   to strip menus, ads, and non-article clutter, and format it for natural
   TTS playback.
3. **Speak** — detects the language with whatlang (English/French, picking
   a matching voice: `gb_jane_neutral` / `fr_marie_neutral`) and generates
   speech with Mistral's TTS API (`voxtral-mini-tts-2603`).
4. **Deliver** — remuxes the MP3 with the ffmpeg CLI so Telegram reports
   the right duration, then replies with the audio message.

Generated audio is cached by URL hash (md5 of the sanitized URL: no
fragment, no query, no trailing slash) in the `generated` directory, so
repeated URLs are served without new API calls. Cached files older than
`CACHE_MAX_AGE_DAYS` (default 30) are pruned at startup.

Every step is logged as structured JSON (received URL, text extracted,
title, cache hit/miss, cleaned, TTS generated, audio delivered).

## Differences from the Python version

- Language detection uses whatlang instead of langdetect; only the
  en/fr voice choice depends on it.
- The `ftfy` mojibake fix on the extracted title was dropped (Go has no
  equivalent; Mistral titles are used as returned).
- ffmpeg is invoked directly as a CLI binary instead of via ffmpeg-python.

## Configuration

Environment variables (all from the `credentials` ExternalSecret in the
in-cluster deployment):

- `TELEGRAM_BOT_TOKEN` — required, bot token
- `MISTRAL_API_KEY` — required, Mistral API access
- `ALLOWED_USERS` — comma-separated Telegram user IDs allowed to use the
  bot (empty = everyone)
- `CACHE_MAX_AGE_DAYS` — optional, cache prune age in days (default 30)
- `SYSTEM_PROMPT_CLEAN` — optional, overrides the text-cleaning prompt
- `SYSTEM_PROMPT_TITLE` — optional, overrides the title-extraction prompt
- `MISTRAL_API_BASE` — optional, overrides the Mistral API endpoint for
  local end-to-end testing

## Deployment

Consumed by `k8s-apps/tts9000` as a Deployment, image
`registry.terence.cloud/tts9000:<tag>` matching `build.yaml`. Bump both
tags together on code changes. The `generated` cache directory is a PVC
mounted at `/app/generated`. The runtime image is alpine-based because
ffmpeg must be present.

## Running

```
go run .
```

## Tests

```
go test ./...
```
