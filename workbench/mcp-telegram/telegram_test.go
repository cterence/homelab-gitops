package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClampText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected string
	}{
		{"short text", "short text", "short text"},
		{"exactly at limit", strings.Repeat("x", 4000), strings.Repeat("x", 4000)},
		{"one over limit", strings.Repeat("x", 4001), strings.Repeat("x", 4000)},
		{"well over limit", strings.Repeat("x", 5000), strings.Repeat("x", 4000)},
		{"caps runes not bytes", strings.Repeat("é", 5000), strings.Repeat("é", 4000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampText(tt.text); got != tt.expected {
				t.Errorf("clampText() = %d chars, want %d chars", len([]rune(got)), len([]rune(tt.expected)))
			}
		})
	}
}

func TestValidParseMode(t *testing.T) {
	tests := []struct {
		mode     string
		expected bool
	}{
		{"", true},
		{"HTML", true},
		{"MarkdownV2", true},
		{"RichText", false},
		{"html", false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if got := validParseMode(tt.mode); got != tt.expected {
				t.Errorf("validParseMode(%q) = %v, want %v", tt.mode, got, tt.expected)
			}
		})
	}
}

// fakeTelegram spins up a server that mimics the Telegram sendMessage API,
// recording the last request it received.
type fakeTelegram struct {
	server   *httptest.Server
	statuses []int // responses to return in order, last one repeats
	calls    int
	path     string
	form     map[string][]string
}

func newFakeTelegram(t *testing.T, statuses ...int) *fakeTelegram {
	t.Helper()

	ft := &fakeTelegram{statuses: statuses}
	if len(ft.statuses) == 0 {
		ft.statuses = []int{http.StatusOK}
	}

	ft.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing form: %v", err)
		}

		ft.calls++
		ft.path = r.URL.Path
		ft.form = r.PostForm

		idx := ft.calls - 1
		if idx >= len(ft.statuses) {
			idx = len(ft.statuses) - 1
		}

		w.WriteHeader(ft.statuses[idx])
		_, _ = w.Write([]byte(`{"ok": true, "result": {"message_id": 1}}`))
	}))
	t.Cleanup(ft.server.Close)

	return ft
}

func testClient(apiBase string) *telegramClient {
	return newTelegramClient(&http.Client{Timeout: telegramTimeout}, apiBase, "123:test-token", "-1001234")
}

func TestDeliverTargetsLockedChat(t *testing.T) {
	ft := newFakeTelegram(t)

	result, err := testClient(ft.server.URL).deliver(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("deliver() error = %v", err)
	}

	if want := `{"ok": true, "result": {"message_id": 1}}`; result != want {
		t.Errorf("deliver() = %q, want %q", result, want)
	}

	if ft.path != "/bot123:test-token/sendMessage" {
		t.Errorf("request path = %q, want /bot123:test-token/sendMessage", ft.path)
	}

	if got := ft.form["chat_id"]; len(got) != 1 || got[0] != "-1001234" {
		t.Errorf("chat_id = %v, want [-1001234]", got)
	}

	if got := ft.form["text"]; len(got) != 1 || got[0] != "hello" {
		t.Errorf("text = %v, want [hello]", got)
	}
}

func TestDeliverIncludesParseMode(t *testing.T) {
	ft := newFakeTelegram(t)

	if _, err := testClient(ft.server.URL).deliver(context.Background(), "<b>hello</b>", "HTML"); err != nil {
		t.Fatalf("deliver() error = %v", err)
	}

	if got := ft.form["parse_mode"]; len(got) != 1 || got[0] != "HTML" {
		t.Errorf("parse_mode = %v, want [HTML]", got)
	}
}

func TestDeliverOmitsEmptyParseMode(t *testing.T) {
	ft := newFakeTelegram(t)

	if _, err := testClient(ft.server.URL).deliver(context.Background(), "hello", ""); err != nil {
		t.Fatalf("deliver() error = %v", err)
	}

	if _, ok := ft.form["parse_mode"]; ok {
		t.Errorf("parse_mode present in form, want omitted")
	}
}

func TestDeliverClampsLongText(t *testing.T) {
	ft := newFakeTelegram(t)

	if _, err := testClient(ft.server.URL).deliver(context.Background(), strings.Repeat("y", 4500), ""); err != nil {
		t.Fatalf("deliver() error = %v", err)
	}

	if got := ft.form["text"][0]; got != strings.Repeat("y", 4000) {
		t.Errorf("text length = %d, want 4000", len(got))
	}
}

func TestDeliverFallsBackToPlainTextOn400(t *testing.T) {
	ft := newFakeTelegram(t, http.StatusBadRequest, http.StatusOK)

	result, err := testClient(ft.server.URL).deliver(context.Background(), "<b>broken</i>", "HTML")
	if err != nil {
		t.Fatalf("deliver() error = %v", err)
	}

	if !strings.Contains(result, `"ok": true`) {
		t.Errorf("deliver() = %q, want trimmed ok response", result)
	}

	if ft.calls != 2 {
		t.Fatalf("calls = %d, want 2", ft.calls)
	}

	if got := ft.form["text"][0]; got != "<b>broken</i>" {
		t.Errorf("text = %q, want original text", got)
	}

	if _, ok := ft.form["parse_mode"]; ok {
		t.Errorf("parse_mode present in retry form, want omitted")
	}
}

func TestDeliverDoesNotRetryNon400Errors(t *testing.T) {
	ft := newFakeTelegram(t, http.StatusForbidden)

	_, err := testClient(ft.server.URL).deliver(context.Background(), "hello", "HTML")
	if err == nil {
		t.Fatal("deliver() error = nil, want Telegram API error")
	}

	if !strings.Contains(err.Error(), "Telegram API error 403") {
		t.Errorf("deliver() error = %v, want it to contain 'Telegram API error 403'", err)
	}

	if ft.calls != 1 {
		t.Errorf("calls = %d, want 1", ft.calls)
	}
}

func TestDeliverWrapsNetworkError(t *testing.T) {
	// A server that is closed before the request is made simulates an
	// unreachable Telegram API.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := s.URL
	s.Close()

	_, err := testClient(url).deliver(context.Background(), "hello", "")
	if err == nil {
		t.Fatal("deliver() error = nil, want unreachable error")
	}

	if !strings.Contains(err.Error(), "telegram API unreachable") {
		t.Errorf("deliver() error = %v, want it to contain 'telegram API unreachable'", err)
	}
}
