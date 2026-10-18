package main

import (
	"context"
	"errors"
	"net"
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

func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"public v4", "1.1.1.1", true},
		{"public v6", "2606:4700::1111", true},
		{"loopback", "127.0.0.1", false},
		{"private v4", "10.0.0.1", false},
		{"private v4 172.16", "172.16.0.1", false},
		{"private v4 192.168", "192.168.1.1", false},
		{"cloud metadata link-local", "169.254.169.254", false},
		{"unspecified", "0.0.0.0", false},
		{"multicast", "224.0.0.1", false},
		{"loopback v6", "::1", false},
		{"link-local v6", "fe80::1", false},
		{"unique local v6", "fd00::1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPublicIP(net.ParseIP(tt.ip)); got != tt.want {
				t.Errorf("isPublicIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestExtractArticleTextRejectsPrivateAddress(t *testing.T) {
	// httptest binds to 127.0.0.1: a bot user pointing the pod at cluster
	// internals must not get the response read back to them.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(articleHTML))
	}))
	t.Cleanup(server.Close)

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = safeDialContext
	client := &http.Client{Timeout: extractTimeout, Transport: transport}

	_, err := extractArticleText(context.Background(), server.URL, client)
	if err == nil {
		t.Fatal("extractArticleText() error = nil, want SSRF rejection for loopback address")
	}

	if !errors.Is(err, errSSRF) {
		t.Errorf("error = %v, want errSSRF", err)
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
