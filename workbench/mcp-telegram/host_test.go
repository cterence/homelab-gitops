package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"localhost", true},
		{"localhost:8000", true},
		{"127.0.0.1", true},
		{"127.0.0.1:8443", true},
		{"[::1]:9000", true},
		{"tmcp.terence.cloud", true},
		{"tmcp.terence.cloud:443", true},
		{"telegram-mcp.snow-delta.ts.net", true},
		{"telegram-mcp.snow-delta.ts.net:8443", true},
		{"", false},
		{"example.com", false},
		{"tmcp.terence.cloud.evil.com", false},
		{"evil.com:8000", false},
		{"[::1]", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := hostAllowed(tt.host); got != tt.expected {
				t.Errorf("hostAllowed(%q) = %v, want %v", tt.host, got, tt.expected)
			}
		})
	}
}

func TestHostGuardMiddleware(t *testing.T) {
	handler := hostGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name     string
		host     string
		expected int
	}{
		{"allowed host", "tmcp.terence.cloud", http.StatusOK},
		{"allowed host with port", "localhost:8080", http.StatusOK},
		{"disallowed host", "evil.com", http.StatusMisdirectedRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Host = tt.host
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expected {
				t.Errorf("status = %d, want %d", rec.Code, tt.expected)
			}
		})
	}
}
