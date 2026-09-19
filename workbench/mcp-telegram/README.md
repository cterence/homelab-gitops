# mcp-telegram

Minimal Telegram notification MCP server. Exposes a single
`send_message(text)` tool that delivers text to the homelab owner's
Telegram chat over the streamable-HTTP transport at `/mcp`.

The recipient is locked server-side: the chat ID comes from the
`TELEGRAM_CHAT_ID` environment variable and is never a tool argument,
so any client (including a prompt-injected task) can only message the
owner. Text is capped at 4000 characters, under Telegram's 4096 limit.

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

## Running

```
uv run python main.py
```

Serves MCP on `0.0.0.0:8000/mcp` and a health probe on `/health`.

## Tests

```
uv run pytest
```
