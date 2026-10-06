package main

// The storer's spool: sealed blobs plus JSON sidecars, written
// atomically, swept by TTL, capped by total bytes.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// spoolMeta is the sidecar describing one parked sealed stream.
type spoolMeta struct {
	ID       string `json:"id"`
	FileName string `json:"fn"`
	SHA      string `json:"sha"`
	From     string `json:"from"`
	Target   string `json:"target"` // member name
	Size     int64  `json:"size"`   // sealed bytes
	At       int64  `json:"at"`     // unix seconds
}

type spool struct{ dir string }

func openSpool(dir string) (*spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	return &spool{dir: dir}, nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)

	return hex.EncodeToString(b)
}

// put spools one sealed stream from src (frames through the zero
// terminator) and records meta; the returned meta carries ID and Size.
func (s *spool) put(src io.Reader, m spoolMeta) (spoolMeta, error) {
	m.ID = newID()
	if m.At == 0 {
		m.At = time.Now().Unix()
	}

	tmp, err := os.CreateTemp(s.dir, ".put-*")
	if err != nil {
		return m, fmt.Errorf("spool temp: %w", err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	size, err := relaySealed(tmp, src)
	if err != nil {
		_ = tmp.Close()

		return m, err
	}

	if err := tmp.Close(); err != nil {
		return m, err
	}

	m.Size = size
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return m, err
	}

	blob := s.blobPath(m.ID)
	if err := os.Rename(tmp.Name(), blob); err != nil {
		return m, fmt.Errorf("spooling %s: %w", blob, err)
	}

	return m, saveJSON(s.metaPath(m.ID), m)
}

func (s *spool) blobPath(id string) string { return filepath.Join(s.dir, id+".blob") }
func (s *spool) metaPath(id string) string { return filepath.Join(s.dir, id+".json") }

// items returns all metas, oldest first.
func (s *spool) items() ([]spoolMeta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading spool: %w", err)
	}

	var metas []spoolMeta

	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}

		m := spoolMeta{}
		if err := loadJSON(s.metaPath(trimExt(e.Name())), &m); err != nil {
			continue // ponytail: orphaned sidecar, sweep will take it
		}

		metas = append(metas, m)
	}
	// Order by deposit time so pulls are deterministic.
	for i := 1; i < len(metas); i++ {
		for j := i; j > 0 && metas[j].At < metas[j-1].At; j-- {
			metas[j], metas[j-1] = metas[j-1], metas[j]
		}
	}

	return metas, nil
}

func trimExt(name string) string { return name[:len(name)-len(filepath.Ext(name))] }

// open returns the meta and blob for id.
func (s *spool) open(id string) (spoolMeta, *os.File, error) {
	m := spoolMeta{}
	if err := loadJSON(s.metaPath(id), &m); err != nil {
		return m, nil, fmt.Errorf("spool meta %s: %w", id, err)
	}

	f, err := os.Open(s.blobPath(id))
	if err != nil {
		return m, nil, fmt.Errorf("spool blob %s: %w", id, err)
	}

	return m, f, nil
}

// delete removes one entry; missing files are not an error.
func (s *spool) delete(id string) error {
	err1 := os.Remove(s.blobPath(id))
	err2 := os.Remove(s.metaPath(id))

	if err1 != nil && !errors.Is(err1, os.ErrNotExist) {
		return err1
	}

	if err2 != nil && !errors.Is(err2, os.ErrNotExist) {
		return err2
	}

	return nil
}

// usage returns the spool's bytes on disk.
func (s *spool) usage() (int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}

	var total int64

	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}

		total += info.Size()
	}

	return total, nil
}

// sweep deletes entries older than ttl.
func (s *spool) sweep(ttl time.Duration) error {
	metas, err := s.items()
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-ttl).Unix()
	for _, m := range metas {
		if m.At < cutoff {
			if err := s.delete(m.ID); err != nil {
				return err
			}
		}
	}

	return nil
}
