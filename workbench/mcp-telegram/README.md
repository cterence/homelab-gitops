# mcp-telegram

Minimal Telegram notification MCP server. Exposes a single
`send_message(text)` tool that delivers text to the homelab owner's
Telegram chat over the streamable-HTTP transport at `/mcp`.

The recipient is locked server-side: the chat ID comes from the
`TELEGRAM_CHAT_ID` environment variable and is never a tool argument,
so any client (including a prompt-injected task) can only message the
owner. Text is capped at 4000 characters, under Telegram's 4096 limit.

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
