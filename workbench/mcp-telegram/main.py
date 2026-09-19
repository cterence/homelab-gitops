"""Minimal Telegram notification MCP server.

Exposes a single send_message tool that delivers text to the homelab
owner's Telegram chat. The recipient is locked server-side via the
TELEGRAM_CHAT_ID environment variable: clients can never choose
another chat, so even a prompt-injected task can only message the
owner.

Messages support Telegram HTML formatting via the parse_mode
parameter ("" | "HTML" | "MarkdownV2"). HTML is preferred because it
is far less brittle than MarkdownV2. On a Telegram 400 (e.g. invalid
HTML from an unescaped character), the server automatically retries
once without parse_mode so messages are never silently lost.
"""

import os
import urllib.error
import urllib.parse
import urllib.request
from contextlib import asynccontextmanager

import uvicorn
from mcp.server import MCPServer
from mcp.server.transport_security import TransportSecuritySettings
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse
from starlette.routing import Mount, Route

TELEGRAM_API_BASE = "https://api.telegram.org"
MAX_TEXT_LENGTH = 4000  # Telegram sendMessage limit is 4096
PARSE_MODES = ("", "HTML", "MarkdownV2")

TELEGRAM_BOT_TOKEN = os.environ["TELEGRAM_BOT_TOKEN"]
TELEGRAM_CHAT_ID = os.environ["TELEGRAM_CHAT_ID"]

mcp = MCPServer("telegram-notify")

def clamp_text(text: str) -> str:
    """Cap text at the Telegram message length limit."""
    return text[:MAX_TEXT_LENGTH]

def build_request(text: str, parse_mode: str = "") -> urllib.request.Request:
    """Build a sendMessage request for the locked chat."""
    payload = {"chat_id": TELEGRAM_CHAT_ID, "text": text}
    if parse_mode:
        payload["parse_mode"] = parse_mode
    return urllib.request.Request(
        f"{TELEGRAM_API_BASE}/bot{TELEGRAM_BOT_TOKEN}/sendMessage",
        data=urllib.parse.urlencode(payload).encode(),
        method="POST",
    )

def deliver(text: str, parse_mode: str = "") -> str:
    """POST a message to Telegram, returning a trimmed API response.

    If Telegram rejects the formatting (HTTP 400), retry once with
    plain text so the message still arrives.
    """
    text = clamp_text(text)
    try:
        return _post(build_request(text, parse_mode))
    except ValueError as err:
        if parse_mode and _is_bad_request(err):
            return _post(build_request(text))
        raise

def _post(request: urllib.request.Request) -> str:
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.read().decode()[:200]
    except urllib.error.HTTPError as err:
        body = err.read().decode(errors="replace")[:200]
        raise ValueError(f"Telegram API error {err.code}: {body}") from err
    except urllib.error.URLError as err:
        raise ValueError(f"Telegram API unreachable: {err.reason}") from err

def _is_bad_request(err: ValueError) -> bool:
    return "Telegram API error 400" in str(err)

@mcp.tool()
def send_message(text: str, parse_mode: str = "") -> str:
    """Send a message to the homelab owner's Telegram chat.

    parse_mode controls Telegram formatting: "" (plain text, default),
    "HTML" (recommended: <b>, <i>, <code>, <pre>), or "MarkdownV2".
    Escape user content with html.escape before wrapping in tags.
    If Telegram rejects the formatted message, it is retried as
    plain text.
    """
    if parse_mode not in PARSE_MODES:
        raise ValueError(f"parse_mode must be one of {PARSE_MODES}, got {parse_mode!r}")
    return deliver(text, parse_mode)

async def health(_request: Request) -> JSONResponse:
    return JSONResponse({"status": "ok"})

@asynccontextmanager
async def lifespan(_app: Starlette):
    async with mcp.session_manager.run():
        yield

mcp_app = mcp.streamable_http_app(
    # Without this the SDK arms DNS-rebinding protection and only accepts
    # localhost Host headers, rejecting every request behind the real
    # hostname with 421 Misdirected Request.
    transport_security=TransportSecuritySettings(
        allowed_hosts=[
            "localhost",
            "localhost:*",
            "127.0.0.1",
            "127.0.0.1:*",
            "[::1]:*",
            "tmcp.terence.cloud",
            "tmcp.terence.cloud:*",
            "telegram-mcp.snow-delta.ts.net",
            "telegram-mcp.snow-delta.ts.net:*",
        ],
    ),
)

app = Starlette(
    routes=[
        Route("/health", health, methods=["GET"]),
        Mount("/", app=mcp_app),
    ],
    lifespan=lifespan,
)

def main() -> None:
    uvicorn.run(app, host="0.0.0.0", port=8000)

if __name__ == "__main__":
    main()

# ci e2e smoke test comment
