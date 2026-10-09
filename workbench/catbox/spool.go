package main

// The storer's spool: sealed blobs plus JSON sidecars, written
// atomically, swept by TTL, capped by total bytes.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// spoolMeta is the sidecar describing one parked sealed stream.
// Plain is the plaintext size for display; Size is the sealed size
// that bounds transfers.
type spoolMeta struct {
	ID       string `json:"id"`
	FileName string `json:"fn"`
	SHA      string `json:"sha"`
	From     string `json:"from"`
	Target   string `json:"target"` // member name
	Plain    int64  `json:"plain"`  // plaintext bytes
	Size     int64  `json:"size"`   // sealed bytes
	At       int64  `json:"at"`     // unix seconds
}

type spool struct {
	dir   string
	log   *slog.Logger
	putMu sync.Map // partial path → *sync.Mutex: same-content deposits serialize
}

// errSpoolFull marks deposits that exceed the spool's remaining byte budget.
var errSpoolFull = errors.New("deposit exceeds the spool's remaining capacity")

// budgetWriter counts bytes written and refuses writes past max, so a
// sender streaming more than it claimed cannot fill the disk.
type budgetWriter struct {
	w   io.Writer
	n   int64
	max int64
}

func (b *budgetWriter) Write(p []byte) (int, error) {
	if b.n+int64(len(p)) > b.max {
		return 0, fmt.Errorf("%w: %d bytes exceeds budget %d", errSpoolFull, b.n+int64(len(p)), b.max)
	}

	n, err := b.w.Write(p)
	b.n += int64(n)

	return n, err
}

func openSpool(dir string, log *slog.Logger) (*spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	return &spool{dir: dir, log: log}, nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)

	return hex.EncodeToString(b)
}

// resumeHave reports the chunk-aligned plaintext prefix already held
// for this content: an interrupted deposit's partial, floored to its
// last whole frame.
func (s *spool) resumeHave(shaHex string, plain int64) int64 {
	if shaHex == "" {
		return 0
	}

	fi, err := os.Stat(partialPath(s.dir, shaHex, plain))
	if err != nil {
		return 0
	}

	sealed := fi.Size() - headerLen
	if sealed < 0 {
		return 0
	}

	have := sealed / chunkFrameLen * chunkSize
	if have >= plain {
		return 0 // a partial as big as the file is corrupt: restart
	}

	return have
}

// put spools one sealed stream from src (frames through the zero
// terminator) and records meta; the returned meta carries ID and Size.
// maxBytes bounds the sealed bytes written, independent of any claimed
// size. With content metadata, the deposit lands in a content-keyed
// partial that survives interruption: have is the caller-advertised
// resume point (see resumeHave), and a failed attempt leaves the
// partial for the retry instead of starting over.
func (s *spool) put(src io.Reader, m spoolMeta, maxBytes, have int64) (spoolMeta, error) {
	m.ID = newID()
	if m.At == 0 {
		m.At = time.Now().Unix()
	}

	if m.SHA == "" {
		tmp, err := os.CreateTemp(s.dir, ".put-*")
		if err != nil {
			return m, fmt.Errorf("spool temp: %w", err)
		}

		defer func() { _ = os.Remove(tmp.Name()) }()

		size, err := relaySealed(&budgetWriter{w: tmp, max: maxBytes}, src)
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

	part := partialPath(s.dir, m.SHA, m.Plain)

	mu, _ := s.putMu.LoadOrStore(part, &sync.Mutex{})
	locked := mu.(*sync.Mutex)
	locked.Lock()
	defer locked.Unlock()

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return m, fmt.Errorf("spool partial: %w", err)
	}

	// The sealed prefix resumes at the advertised chunk: a torn tail
	// is dropped, and a partial that vanished mid-flight fails the
	// deposit rather than corrupting it.
	at := int64(0)
	if have > 0 {
		at = sealedOffset(have / chunkSize)

		fi, err := f.Stat()
		if err != nil || fi.Size() < at {
			_ = f.Close()

			return m, errors.New("deposit partial vanished; retry from zero")
		}
	}

	if err := f.Truncate(at); err != nil {
		_ = f.Close()

		return m, err
	}

	if _, err := f.Seek(at, io.SeekStart); err != nil {
		_ = f.Close()

		return m, err
	}

	_, err = relaySealedAt(&budgetWriter{w: f, max: maxBytes}, src, have)
	if err != nil {
		_ = f.Close()

		return m, err // the partial stays: the retry continues at have
	}

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return m, err
	}

	m.Size = fi.Size()
	if err := f.Close(); err != nil {
		return m, err
	}

	if err := os.Chmod(part, 0o600); err != nil {
		return m, err
	}

	blob := s.blobPath(m.ID)
	if err := os.Rename(part, blob); err != nil {
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
			// Unreadable sidecars can never be pulled or swept: reclaim.
			id := trimExt(e.Name())
			s.log.Warn("unreadable sidecar, reclaiming", "id", id, "err", err)
			_ = s.delete(id)

			continue
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
