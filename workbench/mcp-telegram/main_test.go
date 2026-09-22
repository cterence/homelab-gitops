package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSendMessageToolDeliversToLockedChat(t *testing.T) {
	ft := newFakeTelegram(t)
	handler := newSendMessageHandler(discardLogger(), testClient(ft.server.URL))

	result, _, err := handler(context.Background(), &mcp.CallToolRequest{}, sendMessageInput{Text: "hello world"})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}

	if result.IsError {
		t.Errorf("result.IsError = true, want false")
	}

	if got := ft.form["text"][0]; got != "hello world" {
		t.Errorf("text = %q, want %q", got, "hello world")
	}

	if got := ft.form["chat_id"][0]; got != "-1001234" {
		t.Errorf("chat_id = %q, want -1001234", got)
	}
}

func TestSendMessageToolAcceptsParseMode(t *testing.T) {
	ft := newFakeTelegram(t)
	handler := newSendMessageHandler(discardLogger(), testClient(ft.server.URL))

	_, _, err := handler(context.Background(), &mcp.CallToolRequest{}, sendMessageInput{Text: "<b>bold</b>", ParseMode: "HTML"})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}

	if got := ft.form["parse_mode"][0]; got != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", got)
	}

	if got := ft.form["text"][0]; got != "<b>bold</b>" {
		t.Errorf("text = %q, want <b>bold</b>", got)
	}
}

func TestSendMessageToolRejectsUnknownParseMode(t *testing.T) {
	ft := newFakeTelegram(t)
	handler := newSendMessageHandler(discardLogger(), testClient(ft.server.URL))

	_, _, err := handler(context.Background(), &mcp.CallToolRequest{}, sendMessageInput{Text: "hello", ParseMode: "RichText"})
	if err == nil {
		t.Fatal("handler error = nil, want parse_mode validation error")
	}

	if !strings.Contains(err.Error(), "parse_mode") {
		t.Errorf("handler error = %v, want it to mention parse_mode", err)
	}

	if ft.calls != 0 {
		t.Errorf("calls = %d, want 0 (validation must happen before delivery)", ft.calls)
	}
}

func TestSendMessageToolSurfacesDeliveryFailure(t *testing.T) {
	ft := newFakeTelegram(t, http.StatusForbidden)
	handler := newSendMessageHandler(discardLogger(), testClient(ft.server.URL))

	result, _, err := handler(context.Background(), &mcp.CallToolRequest{}, sendMessageInput{Text: "hello", ParseMode: "HTML"})
	if err == nil {
		t.Fatal("handler error = nil, want delivery error")
	}

	if !strings.Contains(err.Error(), "Telegram API error 403") {
		t.Errorf("handler error = %v, want it to contain 'Telegram API error 403'", err)
	}

	if result != nil && result.IsError {
		t.Logf("result marked as tool error")
	}
}
