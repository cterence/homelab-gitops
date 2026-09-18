import asyncio
import io
import urllib.error
import urllib.parse
from unittest.mock import patch

import pytest

import main
from mcp.client import Client


class FakeResponse:
    def __init__(self, body=b'{"ok": true, "result": {"message_id": 1}}'):
        self.body = body

    def read(self):
        return self.body

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return None


@pytest.mark.parametrize(
    ("text", "expected"),
    [
        ("short text", "short text"),
        ("x" * 4000, "x" * 4000),
        ("x" * 4001, "x" * 4000),
        ("x" * 5000, "x" * 4000),
    ],
)
def test_clamp_text(text, expected):
    assert main.clamp_text(text) == expected


def test_build_request_targets_locked_chat():
    request = main.build_request("hello")
    assert request.full_url == "https://api.telegram.org/bot123:test-token/sendMessage"
    body = urllib.parse.parse_qs(request.data.decode())
    assert body["chat_id"] == ["-1001234"]
    assert body["text"] == ["hello"]


def test_deliver_returns_trimmed_response():
    with patch("urllib.request.urlopen", return_value=FakeResponse()) as fake:
        result = main.deliver("hello")
    assert result == '{"ok": true, "result": {"message_id": 1}}'
    assert fake.call_count == 1


def test_deliver_clamps_long_text():
    with patch("urllib.request.urlopen", return_value=FakeResponse()) as fake:
        main.deliver("y" * 4500)
    body = urllib.parse.parse_qs(fake.call_args[0][0].data.decode())
    assert body["text"] == ["y" * 4000]


def test_deliver_wraps_api_error():
    err = urllib.error.HTTPError("url", 400, "Bad Request", None, io.BytesIO(b'{"ok": false}'))
    with patch("urllib.request.urlopen", side_effect=err):
        with pytest.raises(ValueError, match="Telegram API error 400"):
            main.deliver("hello")


def test_deliver_wraps_network_error():
    err = urllib.error.URLError("connection refused")
    with patch("urllib.request.urlopen", side_effect=err):
        with pytest.raises(ValueError, match="Telegram API unreachable"):
            main.deliver("hello")


def test_send_message_tool_reachable_over_mcp():
    with patch("urllib.request.urlopen", return_value=FakeResponse()) as fake:
        result = asyncio.run(_call_tool("send_message", {"text": "hello world"}))
    assert not getattr(result, "isError", False)
    body = urllib.parse.parse_qs(fake.call_args[0][0].data.decode())
    assert body["text"] == ["hello world"]
    assert body["chat_id"] == ["-1001234"]


async def _call_tool(name, arguments):
    async with Client(main.mcp) as client:
        return await client.call_tool(name, arguments)
