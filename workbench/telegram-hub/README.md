# telegram-hub

The homelab's single Telegram app, written in Go. It merges the old
mcp-telegram and tts9000 apps into one binary that is both:

- an **MCP server** (streamable-HTTP at `/mcp`) exposing two tools:
  - `send_message(text, parse_mode?)` — deliver text to the homelab owner's
    chat. The recipient is locked server-side: the chat ID comes from the
    `TELEGRAM_CHAT_ID` environment variable and is never a tool argument, so
    any client (including a prompt-injected task) can only message the owner.
  - `tts_article(url)` — convert an article to a voice message using
    Mistral's Voxtral TTS and post it to the same locked chat.
- a **long-polling bot**: send the bot a URL in Telegram and it replies with
    the article as an MP3 audio message.

## How the TTS pipeline works

1. **Extract** — fetches the URL and pulls the article text with
   `golang.org/x/net/html` (handles Cloudflare/403 style blocks with a
   clear error). Private/resolved addresses are refused (SSRF guard).
2. **Clean** — sends the raw text to Mistral chat (`mistral-medium-latest`)
   to strip menus, ads, and non-article clutter, and format it for natural
   TTS playback.
3. **Speak** — detects the language with whatlang (English/French, picking
   a matching voice: `gb_jane_neutral` / `fr_marie_neutral`) and generates
   speech with Mistral's TTS API (`voxtral-mini-tts-2603`).
4. **Deliver** — remuxes the MP3 with the ffmpeg CLI so Telegram reports
   the right duration, then sends the audio message.

Generated audio is cached by URL hash (md5 of the sanitized URL: no
fragment, no query, no trailing slash) in the `generated` directory, so
repeated URLs are served without new API calls. Cached files older than
30 days are pruned at startup.

## send_message formatting

`parse_mode` values:

- `""` (default) — plain text
- `"HTML"` — recommended. Supports `<b>`, `<i>`, `<u>`, `<s>`, `<code>`, `<pre>`, `<a href="...">`. Escape user-controlled content (`<`, `>`, `&`) before wrapping it in tags.
- `"MarkdownV2"` — brittle: unescaped `. - ! ( )` etc. cause Telegram to reject the message.

If Telegram rejects a formatted message (HTTP 400), the server
automatically retries once as plain text so notifications are never
silently lost. Unknown `parse_mode` values are rejected with an error.

## Configuration

Environment variables (all from the `credentials` ExternalSecret in the
in-cluster deployment). None are required to start: serve boots with
warnings and each feature fails at call time until its credentials exist.

- `TELEGRAM_BOT_TOKEN` — Telegram bot token (bot loop and delivery tools)
- `TELEGRAM_CHAT_ID` — hard-locked recipient chat for the tools
- `MISTRAL_API_KEY` — Mistral API access (text cleaning and TTS)
- `ALLOWED_USERS` — comma-separated Telegram user IDs allowed to use the
  bot loop; the bot loop is disabled when unset (per-user checks still fail
  closed)
- `SYSTEM_PROMPT_CLEAN` — optional, overrides the text-cleaning prompt
- `TTS_REMUX` — optional; `0` delivers the raw Mistral audio without the
  ffmpeg remux (A/B test: the API's duration header looks fixed)
- `MISTRAL_API_BASE`, `MISTRAL_CHAT_CLEAN_MODEL`, `MISTRAL_CHAT_TITLE_MODEL`,
  `MISTRAL_TTS_MODEL`, `MISTRAL_VOICE_EN`, `MISTRAL_VOICE_FR` — optional,
  override the built-in Mistral defaults

## Deployment notes

- Serves MCP on `:8000/mcp` and a health probe on `:8000/health`.
- The `/mcp` route only accepts requests whose Host header matches the
  allowlist in `host.go` (DNS-rebinding protection); other hosts get
  `421 Misdirected Request`. The `/health` probe route is exempt.
- Exposed at `https://thub.terence.cloud` behind basic auth, and on
  Tailscale as `telegram-hub`.
- The runtime image is alpine-based because ffmpeg must be present.
- The `generated` cache directory is an emptyDir mounted at
  `/app/generated` (cache is disposable; it regenerates from URLs).

## Running

The binary is a small CLI:

```
telegram-hub [serve]                          run the MCP server and bot (default)
telegram-hub tts <url> [-o <file>]           convert an article to MP3 locally
telegram-hub send <text> [--parse-mode <m>]  message the owner chat
```

- `serve` runs the MCP server plus long-polling bot; the deployment passes it
  as the container command. Missing credentials only disable the affected
  features (warnings on stderr).
- `tts` runs the extract → clean → speak pipeline once; by default it prints
  the cache path (`generated/<md5>.mp3`) on stderr. `-o <file>` copies the
  MP3 somewhere, `-o -` streams it to stdout. Needs only `MISTRAL_API_KEY` —
  no Telegram credentials, no bot loop. Precedence is flags over the
  `MISTRAL_*` environment variables over the built-in defaults:
  `--model`, `--clean-model`, `--title-model`, `--api-base`, `--voice-en`
  and `--voice-fr` override the matching variable.
- `send` delivers one message to the `TELEGRAM_CHAT_ID` chat (same locked
  recipient and plain-text retry as the MCP tool). Needs `TELEGRAM_BOT_TOKEN`
  and `TELEGRAM_CHAT_ID`.

Exit codes: 0 success, 1 runtime failure, 2 invalid usage.

## Tests

```
go test ./...
```

`integration_test.go` (real Mistral TTS + ffprobe; compares raw vs remuxed
duration) runs when `TELEGRAM_HUB_INTEGRATION=1` and `MISTRAL_API_KEY` are
set.

## End-to-end test

`e2e/run.sh` builds and starts the server locally, loads the real
credentials from the in-cluster `credentials` secret in the `telegram-hub`
namespace (values never printed), verifies `/health` and the host guard,
then drives `send_message` and `tts_article` over the streamable-HTTP
transport.
