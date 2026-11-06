package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Seen holds the first-seen time of every observed key (pair or dns).
// Only the stream loop touches it, so it needs no locking.
type Seen struct {
	seen  map[string]time.Time
	path  string
	dirty bool
}

func LoadSeen(path string) (*Seen, error) {
	s := &Seen{seen: map[string]time.Time{}, path: path}

	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}

		return nil, fmt.Errorf("reading seen state %s: %w", path, err)
	}

	if err := json.Unmarshal(data, &s.seen); err != nil {
		return nil, fmt.Errorf("parsing seen state %s: %w", path, err)
	}

	return s, nil
}

// Add records the key at first-seen time and returns true when it is novel.
func (s *Seen) Add(key string, now time.Time) bool {
	if _, ok := s.seen[key]; ok {
		return false
	}

	s.seen[key] = now
	s.dirty = true

	return true
}

func (s *Seen) Save() error {
	if !s.dirty {
		return nil
	}

	data, err := json.MarshalIndent(s.seen, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling seen state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("creating seen state directory: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing seen state file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("renaming seen state file: %w", err)
	}

	s.dirty = false

	return nil
}
