package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Registry holds the first-seen time of every observed key (pair or dns).
// Only the stream loop touches it, so it needs no locking.
type Registry struct {
	seen  map[string]time.Time
	path  string
	dirty bool
}

func LoadRegistry(path string) (*Registry, error) {
	r := &Registry{seen: map[string]time.Time{}, path: path}

	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}

		return nil, fmt.Errorf("reading registry %s: %w", path, err)
	}

	if err := json.Unmarshal(data, &r.seen); err != nil {
		return nil, fmt.Errorf("parsing registry %s: %w", path, err)
	}

	return r, nil
}

// Add records the key at first-seen time and returns true when it is novel.
func (r *Registry) Add(key string, now time.Time) bool {
	if _, ok := r.seen[key]; ok {
		return false
	}

	r.seen[key] = now
	r.dirty = true

	return true
}

func (r *Registry) Save() error {
	if !r.dirty {
		return nil
	}

	data, err := json.MarshalIndent(r.seen, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling registry: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("creating registry directory: %w", err)
	}

	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing registry file: %w", err)
	}

	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("renaming registry file: %w", err)
	}

	r.dirty = false

	return nil
}
