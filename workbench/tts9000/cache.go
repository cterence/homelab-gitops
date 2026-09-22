package main

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cacheDir is where generated audio is stored (a PVC at /app/generated in
// the in-cluster deployment).
const cacheDir = "generated"

// sanitizeURL strips the fragment, query, and trailing slash from a URL so
// equivalent URLs share one cache entry.
func sanitizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	parsed.RawFragment = ""
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return parsed.String()
}

// getCacheFilename returns the cache path for a URL: generated/<md5>.mp3.
func getCacheFilename(rawURL string) string {
	cleanURL := sanitizeURL(rawURL)
	sum := md5.Sum([]byte(cleanURL)) //nolint:gosec // cache key only, not security

	return filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".mp3")
}

// pruneCache deletes cached audio files older than maxAgeDays. Individual
// deletion failures are joined and returned; the walk continues either way.
func pruneCache(dir string, maxAgeDays int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("reading cache dir %s: %w", dir, err)
	}

	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)

	var errs []error

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".mp3" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			errs = append(errs, fmt.Errorf("statting %s: %w", entry.Name(), err))
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				errs = append(errs, fmt.Errorf("pruning %s: %w", path, err))
			}
		}
	}

	return errors.Join(errs...)
}
