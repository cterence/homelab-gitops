package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"
)

func TestJSONPath(t *testing.T) {
	payload := map[string]any{
		"alerts": []any{
			map[string]any{"labels": map[string]any{"alertname": "HighCPU"}},
		},
		"status": "firing",
	}

	tests := []struct {
		name  string
		path  string
		want  string
		wantB bool
	}{
		{"nested with array index", "alerts.0.labels.alertname", "HighCPU", true},
		{"simple top-level", "status", "firing", true},
		{"missing key", "alerts.0.labels.namespace", "", false},
		{"array out of range", "alerts.1.labels.alertname", "", false},
		{"non-numeric array index", "alerts.x", "", false},
		{"non-string leaf", "alerts", "", false},
		{"empty path", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := jsonPath(payload, tt.path)
			if got != tt.want || ok != tt.wantB {
				t.Errorf("jsonPath(%q) = (%q, %v), want (%q, %v)", tt.path, got, ok, tt.want, tt.wantB)
			}
		})
	}
}

func TestClaim(t *testing.T) {
	rl := &relay{}
	now := time.Now()

	if !rl.claim("k", 15*time.Minute, now) {
		t.Error("first claim should be allowed")
	}

	if rl.claim("k", 15*time.Minute, now.Add(5*time.Minute)) {
		t.Error("claim inside window should be rejected")
	}

	if !rl.claim("k", 15*time.Minute, now.Add(20*time.Minute)) {
		t.Error("claim outside window should be allowed")
	}

	if !rl.claim("other", 15*time.Minute, now) {
		t.Error("distinct key should be allowed")
	}
}

type recordingInvoker struct {
	mu       sync.Mutex
	prompts  []string
	convName []string
	done     chan struct{}
}

func newRecordingInvoker() *recordingInvoker {
	return &recordingInvoker{done: make(chan struct{}, 16)}
}

func (ri *recordingInvoker) call(_ context.Context, _, _, conversationName, prompt string) (string, error) {
	ri.mu.Lock()
	ri.prompts = append(ri.prompts, prompt)
	ri.convName = append(ri.convName, conversationName)
	ri.mu.Unlock()

	ri.done <- struct{}{}

	return "conv_test", nil
}

// waitFor blocks until n invocations completed, or fails the test.
func (ri *recordingInvoker) waitFor(t *testing.T, n int) {
	t.Helper()

	for range n {
		select {
		case <-ri.done:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for invoker call %d", n)
		}
	}
}

func testRelay(inv *recordingInvoker) *relay {
	return &relay{
		logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		invoker: inv.call,
	}
}

func testRoute(t *testing.T) route {
	t.Helper()

	r := route{
		Path:             "/alerts",
		AgentID:          "ag_test",
		ConversationName: "alert-triage",
		Prompt:           "Alert fired:\n{{.JSON}}",
		DedupKey:         "alerts.0.labels.alertname",
		window:           15 * time.Minute,
	}

	tpl, err := template.New("prompt").Parse(r.Prompt)
	if err != nil {
		t.Fatalf("parsing test prompt: %v", err)
	}

	r.promptTpl = tpl

	return r
}

func TestHandlerInvokesAgent(t *testing.T) {
	inv := newRecordingInvoker()
	rl := testRelay(inv)
	r := testRoute(t)

	body := `{"status":"firing","alerts":[{"labels":{"alertname":"HighCPU"}}]}`
	req := httptest.NewRequest(http.MethodPost, r.Path, strings.NewReader(body))
	rec := httptest.NewRecorder()

	rl.handler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}

	inv.waitFor(t, 1)
	inv.mu.Lock()
	defer inv.mu.Unlock()

	if !strings.Contains(inv.prompts[0], "HighCPU") || !strings.Contains(inv.prompts[0], "\n  \"alerts\"") {
		t.Errorf("prompt missing pretty-printed payload: %q", inv.prompts[0])
	}

	if inv.convName[0] != "alert-triage" {
		t.Errorf("conversation name = %q", inv.convName[0])
	}
}

func TestHandlerDeduplicates(t *testing.T) {
	inv := newRecordingInvoker()
	rl := testRelay(inv)
	r := testRoute(t)

	body := `{"alerts":[{"labels":{"alertname":"HighCPU"}}]}`
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, r.Path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		rl.handler(r).ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("status = %d, want 202", rec.Code)
		}
	}

	inv.waitFor(t, 1)
	inv.mu.Lock()
	defer inv.mu.Unlock()

	if len(inv.prompts) != 1 {
		t.Errorf("invoked %d times, want 1", len(inv.prompts))
	}
}

func TestHandlerDistinctKeysBothRun(t *testing.T) {
	inv := newRecordingInvoker()
	rl := testRelay(inv)
	r := testRoute(t)

	for _, name := range []string{"HighCPU", "HighMemory"} {
		body := `{"alerts":[{"labels":{"alertname":"` + name + `"}}]}`
		req := httptest.NewRequest(http.MethodPost, r.Path, strings.NewReader(body))
		rl.handler(r).ServeHTTP(httptest.NewRecorder(), req)
	}

	inv.waitFor(t, 2)
}

func TestHandlerInvalidJSON(t *testing.T) {
	inv := newRecordingInvoker()
	rl := testRelay(inv)
	r := testRoute(t)

	req := httptest.NewRequest(http.MethodPost, r.Path, strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	rl.handler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	select {
	case <-inv.done:
		t.Error("invoker must not be called on invalid JSON")
	default:
	}
}
