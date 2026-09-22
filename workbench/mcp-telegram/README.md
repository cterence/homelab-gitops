# mcp-telegram

Minimal Telegram notification MCP server, written in Go on the official
MCP Go SDK. Exposes a single `send_message(text)` tool that delivers text
to the homelab owner's Telegram chat over the streamable-HTTP transport
at `/mcp`.

The recipient is locked server-side: the chat ID comes from the
`TELEGRAM_CHAT_ID` environment variable and is never a tool argument,
so any client (including a prompt-injected task) can only message the
owner. Text is capped at 4000 characters (rune-safe), under Telegram's
4096 limit.

## Formatting

`send_message` accepts an optional `parse_mode` argument:

- `""` (default) — plain text
- `"HTML"` — recommended. Supports `<b>`, `<i>`, `<u>`, `<s>`, `<code>`, `<pre>`, `<a href="...">`. Escape user-controlled content (`<`, `>`, `&`) before wrapping it in tags.
- `"MarkdownV2"` — brittle: unescaped `. - ! ( )` etc. cause Telegram to reject the message.

If Telegram rejects a formatted message (HTTP 400), the server
automatically retries once as plain text so notifications are never
silently lost. Unknown `parse_mode` values are rejected with an error.

## Configuration

Both variables are required and fail fast at startup:

- `TELEGRAM_BOT_TOKEN` — dedicated bot token
- `TELEGRAM_CHAT_ID` — hard-locked recipient chat

Optional:

- `TELEGRAM_API_BASE` — override the Telegram API endpoint (defaults to
  `https://api.telegram.org`); useful for local end-to-end testing against a
  fake API.

## Deployment notes

- Serves MCP on `:8000/mcp` and a health probe on `:8000/health`.
- The `/mcp` route only accepts requests whose Host header matches the
  allowlist in `host.go` (DNS-rebinding protection, mirroring the Python
  SDK's TransportSecuritySettings); other hosts get `421 Misdirected
  Request`. The `/health` probe route is exempt.
- MCP tool errors and delivery failures are returned as protocol-level
  errors, matching the previous Python behavior.

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
credentials from the in-cluster `credentials` secret in the `mcp-telegram`
namespace (values never printed), verifies `/health` and the host guard,
then drives `send_message` over the streamable-HTTP transport: plain text,
valid HTML, broken HTML (exercises the 400 retry-as-plain-text fallback
against the real Telegram API), and an invalid `parse_mode` (rejected).

```
./e2e/run.sh          # needs kubectl with homelab cluster access
```
