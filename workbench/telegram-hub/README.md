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
in-cluster deployment):

- `TELEGRAM_BOT_TOKEN` — required, the bot token (one bot does everything)
- `TELEGRAM_CHAT_ID` — required, hard-locked recipient chat for the tools
- `MISTRAL_API_KEY` — required, Mistral API access
- `ALLOWED_USERS` — required, comma-separated Telegram user IDs allowed to
  use the bot loop; the app refuses to start when unset (fail closed)
- `SYSTEM_PROMPT_CLEAN` — optional, overrides the text-cleaning prompt

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

```
go run .
```

## Tests

```
go test ./...
```

## End-to-end test

`e2e/run.sh` builds and starts the server locally, loads the real
credentials from the in-cluster `credentials` secret in the `telegram-hub`
namespace (values never printed), verifies `/health` and the host guard,
then drives `send_message` and `tts_article` over the streamable-HTTP
transport.
