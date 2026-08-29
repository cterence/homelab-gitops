package traefikjail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlugin_BannedIPGets403(t *testing.T) {
	plugin := &JailPlugin{
		jailer: NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
	}

	// First request with a 404 response — threshold is 1, so this triggers a ban
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec := httptest.NewRecorder()

	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("first request: expected 404, got %d", rec.Code)
	}

	// Second request from the same IP — should be banned, get 403
	req2 := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req2.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec2 := httptest.NewRecorder()

	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("banned request: expected 403, got %d", rec2.Code)
	}
}

func TestPlugin_NonBannedIPPassthrough(t *testing.T) {
	plugin := &JailPlugin{
		jailer: NewJailer(10, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "5.6.7.8")

	rec := httptest.NewRecorder()

	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 passthrough, got %d", rec.Code)
	}
}

func TestPlugin_5xxNotCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer: NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
	}

	// 500 should not trigger a ban (only 4xx)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "9.9.9.9")

	rec := httptest.NewRecorder()

	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 passthrough, got %d", rec.Code)
	}

	// Should not be banned
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Forwarded-For", "9.9.9.9")

	rec2 := httptest.NewRecorder()
	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 passthrough (not banned), got %d", rec2.Code)
	}
}

func TestPlugin_2xxNotCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer: NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	plugin.next = next

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "8.8.8.8")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func TestNew_ValidConfig(t *testing.T) {
	cfg := &Config{
		Threshold:  5,
		Window:     30,
		BaseBan:    120,
		MaxBan:     7200,
		ResetAfter: 1800,
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})

	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if h == nil {
		t.Fatal("New() returned nil handler")
	}
}

func TestCreateConfig_Defaults(t *testing.T) {
	cfg := CreateConfig()

	if cfg.Threshold != 10 {
		t.Errorf("Threshold = %d, want 10", cfg.Threshold)
	}

	if cfg.Window != 60 {
		t.Errorf("Window = %d, want 60", cfg.Window)
	}

	if cfg.BaseBan != 60 {
		t.Errorf("BaseBan = %d, want 60", cfg.BaseBan)
	}

	if cfg.MaxBan != 3600 {
		t.Errorf("MaxBan = %d, want 3600", cfg.MaxBan)
	}

	if cfg.ResetAfter != 3600 {
		t.Errorf("ResetAfter = %d, want 3600", cfg.ResetAfter)
	}
}
