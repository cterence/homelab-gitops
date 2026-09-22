package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testLogger returns a logger writing to a buffer, plus a reader for it.
func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRequestLogMiddleware(t *testing.T) {
	logger, buf := testLogger()

	handler := requestLog(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (middleware must not alter responses)", rec.Code)
	}

	out := buf.String()
	for _, want := range []string{`"msg":"request"`, `"method":"POST"`, `"path":"/mcp"`, `"status":200`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s:\n%s", want, out)
		}
	}
}

func TestRequestLogPreservesFlusher(t *testing.T) {
	// Streamable-HTTP responses are SSE streams; the handler must be able
	// to flush through the status-recording wrapper.
	var flusherOK bool

	handler := requestLog(discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, flusherOK = w.(http.Flusher)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if !flusherOK {
		t.Error("ResponseWriter passed to handler does not implement http.Flusher")
	}
}

func TestSendMessageHandlerLogsActions(t *testing.T) {
	ft := newFakeTelegram(t)
	logger, buf := testLogger()
	handler := newSendMessageHandler(logger, testClient(ft.server.URL))

	if _, _, err := handler(context.Background(), &mcp.CallToolRequest{}, sendMessageInput{Text: "hello"}); err != nil {
		t.Fatalf("handler error = %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		`"msg":"send_message tool called"`,
		`"text_length":5`,
		`"msg":"message delivered"`,
		`"parse_mode":""`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s:\n%s", want, out)
		}
	}
}

func TestSendMessageHandlerLogsDeliveryFailure(t *testing.T) {
	ft := newFakeTelegram(t, http.StatusForbidden)
	logger, buf := testLogger()
	handler := newSendMessageHandler(logger, testClient(ft.server.URL))

	if _, _, err := handler(context.Background(), &mcp.CallToolRequest{}, sendMessageInput{Text: "hello"}); err == nil {
		t.Fatal("handler error = nil, want delivery error")
	}

	if !strings.Contains(buf.String(), `"msg":"delivery failed"`) {
		t.Errorf("log output missing delivery failure:\n%s", buf.String())
	}
}

func TestDeliverLogsPlainTextInputRetry(t *testing.T) {
	ft := newFakeTelegram(t, http.StatusBadRequest, http.StatusOK)
	logger, buf := testLogger()
	client := testClient(ft.server.URL).withLogger(logger)

	if _, err := client.deliver(context.Background(), "<b>broken</i>", "HTML"); err != nil {
		t.Fatalf("deliver() error = %v", err)
	}

	if !strings.Contains(buf.String(), `"msg":"retrying as plain text after 400"`) {
		t.Errorf("log output missing retry:\n%s", buf.String())
	}
}

func TestHostGuardLogsRejection(t *testing.T) {
	logger, buf := testLogger()
	handler := hostGuard(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "evil.com"
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want 421", rec.Code)
	}

	out := buf.String()
	for _, want := range []string{`"msg":"rejected host"`, `"host":"evil.com"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s:\n%s", want, out)
		}
	}
}
