# tts9000

Telegram bot that converts articles to audio using Mistral's Voxtral TTS.
Send it a URL and it replies with the article as an MP3 voice message.

Migrated from https://github.com/cterence/tts9000.

## How it works

1. **Extract** — fetches the URL and pulls the article text with
   BeautifulSoup (handles Cloudflare/403 style blocks with a clear error).
2. **Clean** — sends the raw text to Mistral to strip menus, ads, and
   non-article clutter, and format it for natural TTS playback.
3. **Speak** — detects the language (English/French, picking a matching
   voice) and generates speech with Mistral's TTS API.
4. **Deliver** — fixes the MP3 header with ffmpeg so Telegram reports the
   right duration, then replies with the audio message.

Generated audio is cached by URL hash in the `generated` directory, so
repeated URLs are served without new API calls.

## Configuration

Environment variables (all from the `credentials` ExternalSecret in the
in-cluster deployment):

- `TELEGRAM_BOT_TOKEN` — required, bot token
- `MISTRAL_API_KEY` — required, Mistral API access
- `ALLOWED_USERS` — comma-separated Telegram user IDs allowed to use the
  bot (empty = everyone)
- `SYSTEM_PROMPT_CLEAN` — optional, overrides the text-cleaning prompt
- `SYSTEM_PROMPT_TITLE` — optional, overrides the title-extraction prompt

## Deployment

Consumed by `k8s-apps/tts9000` as a Deployment, image
`registry.terence.cloud/tts9000:<tag>` matching `build.yaml`. Bump both
tags together on code changes. The `generated` cache directory is a PVC
mounted at `/app/generated`.
