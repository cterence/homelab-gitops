"""Minimal Telegram notification MCP server.

Exposes a single send_message tool that delivers text to the homelab
owner's Telegram chat. The recipient is locked server-side via the
TELEGRAM_CHAT_ID environment variable: clients can never choose
another chat, so even a prompt-injected task can only message the
owner.
"""

import os
import urllib.error
import urllib.parse
import urllib.request
from contextlib import asynccontextmanager

import uvicorn
from mcp.server import MCPServer
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse
from starlette.routing import Mount, Route

TELEGRAM_API_BASE = "https://api.telegram.org"
MAX_TEXT_LENGTH = 4000  # Telegram sendMessage limit is 4096

TELEGRAM_BOT_TOKEN = os.environ["TELEGRAM_BOT_TOKEN"]
TELEGRAM_CHAT_ID = os.environ["TELEGRAM_CHAT_ID"]

mcp = MCPServer("telegram-notify")


def clamp_text(text: str) -> str:
    """Cap text at the Telegram message length limit."""
    return text[:MAX_TEXT_LENGTH]


def build_request(text: str) -> urllib.request.Request:
    """Build a sendMessage request for the locked chat."""
    payload = urllib.parse.urlencode({"chat_id": TELEGRAM_CHAT_ID, "text": text})
    return urllib.request.Request(
        f"{TELEGRAM_API_BASE}/bot{TELEGRAM_BOT_TOKEN}/sendMessage",
        data=payload.encode(),
        method="POST",
    )


def deliver(text: str) -> str:
    """POST a message to Telegram, returning a trimmed API response."""
    try:
        with urllib.request.urlopen(build_request(clamp_text(text)), timeout=10) as response:
            return response.read().decode()[:200]
    except urllib.error.HTTPError as err:
        body = err.read().decode(errors="replace")[:200]
        raise ValueError(f"Telegram API error {err.code}: {body}") from err
    except urllib.error.URLError as err:
        raise ValueError(f"Telegram API unreachable: {err.reason}") from err


@mcp.tool()
def send_message(text: str) -> str:
    """Send a message to the homelab owner's Telegram chat."""
    return deliver(text)


async def health(_request: Request) -> JSONResponse:
    return JSONResponse({"status": "ok"})


@asynccontextmanager
async def lifespan(_app: Starlette):
    async with mcp.session_manager.run():
        yield


mcp_app = mcp.streamable_http_app()

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
