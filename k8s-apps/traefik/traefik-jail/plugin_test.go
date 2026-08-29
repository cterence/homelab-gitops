package traefikjail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlugin_BannedIPGets403(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
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
		jailer:     NewJailer(10, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
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
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
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
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
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

func TestPlugin_AllowedIPSkipsJail(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		allowList:  []string{"10.0.0.0/8"},
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// Even with threshold=1 and 404 responses, allowed IP should never be jailed
	for range 20 {
		req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0.5")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("allowed IP should pass through, got %d", rec.Code)
		}
	}

	if calls != 20 {
		t.Fatalf("expected 20 passthroughs, got %d", calls)
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

func TestCodeMatcher(t *testing.T) {
	tests := []struct {
		name   string
		config string
		status int
		want   bool
	}{
		{"range match", "400-499", 404, true},
		{"range no match", "400-499", 500, false},
		{"single match", "404", 404, true},
		{"single no match", "404", 403, false},
		{"mixed range and single match range", "404,500-503", 502, true},
		{"mixed range and single match single", "404,500-503", 404, true},
		{"mixed no match", "404,500-503", 403, false},
		{"empty config no match", "", 404, false},
		{"invalid code ignored", "404,abc,500", 500, true},
		{"whitespace trimmed", " 404 , 500-503 ", 404, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := parseErrorCodes(tt.config)
			if m.matches(tt.status) != tt.want {
				t.Errorf("parseErrorCodes(%q).matches(%d) = %v, want %v",
					tt.config, tt.status, m.matches(tt.status), tt.want)
			}
		})
	}
}

func TestPlugin_CustomErrorCodes_5xxCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("500-503"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "7.7.7.7")

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 passthrough, got %d", rec.Code)
	}

	// Should be banned now (500 matches custom config)
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Forwarded-For", "7.7.7.7")

	rec2 := httptest.NewRecorder()
	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (banned), got %d", rec2.Code)
	}
}

func TestPlugin_CustomErrorCodes_4xxNotCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("500-503"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "6.6.6.6")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 passthrough, got %d", rec.Code)
		}
	}
}
