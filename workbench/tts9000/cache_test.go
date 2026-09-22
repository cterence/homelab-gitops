package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"plain url", "https://example.com/article", "https://example.com/article"},
		{"trailing slash", "https://example.com/article/", "https://example.com/article"},
		{"root only", "https://example.com/", "https://example.com"},
		{"query removed", "https://example.com/a?utm_source=x", "https://example.com/a"},
		{"fragment removed", "https://example.com/a#section", "https://example.com/a"},
		{"query and fragment", "https://example.com/a/?q=1#top", "https://example.com/a"},
		{"no trailing slash on root-less path", "https://example.com", "https://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeURL(tt.url); got != tt.expected {
				t.Errorf("sanitizeURL(%q) = %q, want %q", tt.url, got, tt.expected)
			}
		})
	}
}

func TestGetCacheFilename(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"plain url", "https://example.com/article"},
		{"same url with noise", "https://example.com/article?utm=x#frag"},
	}

	// The noisy URL must hash to the same file as the clean one.
	first := getCacheFilename(tests[0].url)

	second := getCacheFilename(tests[1].url)
	if first != second {
		t.Errorf("cache filenames differ for equivalent URLs: %q vs %q", first, second)
	}

	if filepath.Dir(first) != "generated" {
		t.Errorf("cache file not in generated/ dir: %q", first)
	}

	if filepath.Ext(first) != ".mp3" {
		t.Errorf("cache file not .mp3: %q", first)
	}
}

func TestPruneCacheRemovesOldFiles(t *testing.T) {
	dir := t.TempDir()

	oldFile := filepath.Join(dir, "old.mp3")
	if err := os.WriteFile(oldFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	recentFile := filepath.Join(dir, "recent.mp3")
	if err := os.WriteFile(recentFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := pruneCache(dir, 30); err != nil {
		t.Fatalf("pruneCache() error = %v", err)
	}

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("old file still exists")
	}

	if _, err := os.Stat(recentFile); err != nil {
		t.Errorf("recent file was pruned: %v", err)
	}
}

func TestPruneCacheKeepsFreshFiles(t *testing.T) {
	dir := t.TempDir()

	freshFile := filepath.Join(dir, "fresh.mp3")
	if err := os.WriteFile(freshFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := pruneCache(dir, 30); err != nil {
		t.Fatalf("pruneCache() error = %v", err)
	}

	if _, err := os.Stat(freshFile); err != nil {
		t.Errorf("fresh file was pruned: %v", err)
	}
}

func TestPruneCacheMissingDir(t *testing.T) {
	if err := pruneCache(filepath.Join(t.TempDir(), "nonexistent"), 30); err != nil {
		t.Errorf("pruneCache() on missing dir error = %v, want nil", err)
	}
}

func TestPruneCacheIgnoresNonMp3Files(t *testing.T) {
	dir := t.TempDir()

	otherFile := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(otherFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(otherFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	if err := pruneCache(dir, 30); err != nil {
		t.Fatalf("pruneCache() error = %v", err)
	}

	if _, err := os.Stat(otherFile); err != nil {
		t.Errorf("non-mp3 file was pruned: %v", err)
	}
}
