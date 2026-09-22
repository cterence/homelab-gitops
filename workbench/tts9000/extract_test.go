package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const articleHTML = `<!DOCTYPE html>
<html><head><title>Test Article</title><script>var x = 1;</script></head>
<body>
<nav>Home About Contact</nav>
<article><h1>Big headline</h1><p>First paragraph of the article.</p><p>Second paragraph.</p></article>
<footer>Copyright notice</footer>
</body></html>`

func TestExtractArticleText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("request missing User-Agent header")
		}

		_, _ = w.Write([]byte(articleHTML))
	}))
	t.Cleanup(server.Close)

	text, err := extractArticleText(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("extractArticleText() error = %v", err)
	}

	for _, want := range []string{"Big headline", "First paragraph of the article.", "Second paragraph."} {
		if !strings.Contains(text, want) {
			t.Errorf("extracted text missing %q:\n%s", want, text)
		}
	}
}

func TestExtractArticleTextErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantSubstr string
		wantBlock  bool
	}{
		{"403 is blocked", 403, "access blocked", true},
		{"404 is client error", 404, "client error (404)", false},
		{"500 is server error", 500, "server error (500)", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(server.Close)

			_, err := extractArticleText(context.Background(), server.URL, server.Client())
			if err == nil {
				t.Fatal("extractArticleText() error = nil, want error")
			}

			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("extractArticleText() error = %v, want it to contain %q", err, tt.wantSubstr)
			}

			if errors.Is(err, errBlocked) != tt.wantBlock {
				t.Errorf("errors.Is(err, errBlocked) = %v, want %v", errors.Is(err, errBlocked), tt.wantBlock)
			}
		})
	}
}

func TestExtractArticleTextNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	_, err := extractArticleText(context.Background(), url, server.Client())
	if err == nil {
		t.Fatal("extractArticleText() error = nil, want request-failed error")
	}

	if !strings.Contains(err.Error(), "request failed") {
		t.Errorf("extractArticleText() error = %v, want it to contain 'request failed'", err)
	}
}
