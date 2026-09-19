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


def test_build_request_includes_parse_mode():
    request = main.build_request("<b>hello</b>", "HTML")
    body = urllib.parse.parse_qs(request.data.decode())
    assert body["parse_mode"] == ["HTML"]


def test_build_request_omits_empty_parse_mode():
    request = main.build_request("hello", "")
    body = urllib.parse.parse_qs(request.data.decode())
    assert "parse_mode" not in body


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


def test_deliver_falls_back_to_plain_text_on_400():
    err = urllib.error.HTTPError("url", 400, "Bad Request", None, io.BytesIO(b'{"ok": false}'))
    with patch("urllib.request.urlopen", side_effect=[err, FakeResponse()]) as fake:
        result = main.deliver("<b>broken</i>", "HTML")
    assert '"ok": true' in result
    assert fake.call_count == 2
    second_body = urllib.parse.parse_qs(fake.call_args[0][0].data.decode())
    assert second_body["text"] == ["<b>broken</i>"]
    assert "parse_mode" not in second_body


def test_deliver_does_not_retry_non_400_errors():
    err = urllib.error.HTTPError("url", 403, "Forbidden", None, io.BytesIO(b'{"ok": false}'))
    with patch("urllib.request.urlopen", side_effect=err) as fake:
        with pytest.raises(ValueError, match="Telegram API error 403"):
            main.deliver("hello", "HTML")
    assert fake.call_count == 1


def test_send_message_rejects_unknown_parse_mode():
    with pytest.raises(ValueError, match="parse_mode"):
        main.send_message("hello", parse_mode="RichText")


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


def test_send_message_tool_accepts_parse_mode():
    with patch("urllib.request.urlopen", return_value=FakeResponse()) as fake:
        result = asyncio.run(_call_tool("send_message", {"text": "<b>bold</b>", "parse_mode": "HTML"}))
    assert not getattr(result, "isError", False)
    body = urllib.parse.parse_qs(fake.call_args[0][0].data.decode())
    assert body["parse_mode"] == ["HTML"]
    assert body["text"] == ["<b>bold</b>"]


async def _call_tool(name, arguments):
    async with Client(main.mcp) as client:
        return await client.call_tool(name, arguments)
