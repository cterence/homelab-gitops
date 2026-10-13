package main

// The stash's spool: sealed blobs plus JSON sidecars, written
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
	"strings"
	"sync"
	"time"
)

// spoolMeta is the sidecar describing one parked delivery. Plain is
// the plaintext size for display; Size is the sealed size that
// bounds transfers. Header holds this target's sealed header when
// the entry rides a shared body; nil means the header sits inline in
// the entry's own blob.
type spoolMeta struct {
	ID       string `json:"id"`
	FileName string `json:"fn"`
	SHA      string `json:"sha"`
	From     string `json:"from"`
	Target   string `json:"target"` // member name
	Plain    int64  `json:"plain"`  // plaintext bytes
	Size     int64  `json:"size"`   // sealed bytes
	At       int64  `json:"at"`     // unix seconds
	Header   []byte `json:"hdr,omitempty"`
}

type spool struct {
	dir    string
	log    *slog.Logger
	putMu  sync.Map // partial path → *sync.Mutex: same-content deposits serialize
	active sync.Map // partial path → io.Closer: the deposit writing it; the next one evicts
}

// claim marks c as the deposit writing this content's partial; the
// next deposit of the same content closes it instead of waiting out
// the partial lock behind a dead stream.
func (s *spool) claim(from, shaHex string, plain int64, c io.Closer) {
	if shaHex == "" {
		return
	}

	if prev, loaded := s.active.Swap(spoolPartialPath(s.dir, from, shaHex, plain), c); loaded {
		if old, ok := prev.(io.Closer); ok {
			_ = old.Close()
		}
	}
}

// release forgets a claim; a replaced deposit is a no-op.
func (s *spool) release(from, shaHex string, plain int64, c io.Closer) {
	if shaHex == "" {
		return
	}

	s.active.CompareAndDelete(spoolPartialPath(s.dir, from, shaHex, plain), c)
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

// spoolBodyPath names the body parked for one sender's content: the
// sealed frames without any header, shared by every target the file
// was sent to. Member names are validated lowercase slugs, so they
// are safe in a filename.
func spoolBodyPath(dir, from, shaHex string, plain int64) string {
	return filepath.Join(dir, fmt.Sprintf("b-%s-%s-%d.body", from, shaHex[:12], plain))
}

// spoolPartialPath names a deposit partial. The -b marker separates
// it from pre-shared-body partials, whose ciphertext is
// recipient-keyed and must never be resumed into a shared body.
func spoolPartialPath(dir, from, shaHex string, plain int64) string {
	return filepath.Join(dir, fmt.Sprintf(".part-b-%s-%s-%d", from, shaHex[:12], plain))
}

// resumeHave reports the chunk-aligned plaintext prefix already held
// for this content: an interrupted deposit's partial, floored to its
// last whole frame.
func (s *spool) resumeHave(from, shaHex string, plain int64) int64 {
	if shaHex == "" {
		return 0
	}

	fi, err := os.Stat(spoolPartialPath(s.dir, from, shaHex, plain))
	if err != nil {
		return 0
	}

	have := fi.Size() / chunkFrameLen * chunkSize
	if have >= plain {
		return 0 // a partial as big as the file is corrupt: restart
	}

	return have
}

// bodyParked reports whether this sender's body for the content is
// already complete: bodies only appear via an atomic rename of a
// finished partial, so existence means whole.
func (s *spool) bodyParked(from, shaHex string, plain int64) bool {
	if shaHex == "" {
		return false
	}

	_, err := os.Stat(spoolBodyPath(s.dir, from, shaHex, plain))

	return err == nil
}

// put spools one deposit from src (the full sealed stream: frames
// through the zero terminator) and records meta; the returned meta
// carries ID and Size. The recipient-specific header lands in the
// meta; the frames land in the sender's content-keyed body, shared
// with any other target of the same file. maxBytes bounds the sealed
// bytes written, independent of any claimed size. have is the
// caller-advertised resume point (see resumeHave); a failed attempt
// leaves the partial for the retry instead of starting over.
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

	mu, _ := s.putMu.LoadOrStore(spoolPartialPath(s.dir, m.From, m.SHA, m.Plain), &sync.Mutex{})
	locked := mu.(*sync.Mutex)
	locked.Lock()
	defer locked.Unlock()

	f, err := os.OpenFile(spoolPartialPath(s.dir, m.From, m.SHA, m.Plain), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return m, fmt.Errorf("spool partial: %w", err)
	}

	// The sealed prefix resumes at the advertised chunk: a torn tail
	// is dropped, and a partial that vanished mid-flight fails the
	// deposit rather than corrupting it.
	at := int64(0)
	if have > 0 {
		at = bodyOffset(have / chunkSize)

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

	// The header is per-target: keep it in the meta, relay the frames.
	hdr := make([]byte, headerLen)
	if _, err := io.ReadFull(src, hdr); err != nil {
		_ = f.Close()

		return m, fmt.Errorf("%w: header cut short", errCorrupt)
	}

	if _, err := relayFrames(&budgetWriter{w: f, max: maxBytes}, src, have); err != nil {
		_ = f.Close()

		return m, err // the partial stays: the retry continues at have
	}

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return m, err
	}

	m.Header = hdr

	m.Size = headerLen + fi.Size()
	if err := f.Close(); err != nil {
		return m, err
	}

	if err := os.Chmod(f.Name(), 0o600); err != nil {
		return m, err
	}

	body := spoolBodyPath(s.dir, m.From, m.SHA, m.Plain)
	if _, err := os.Stat(body); err == nil {
		// Another target's deposit finished first and parked this
		// body: ours is a duplicate, drop it.
		_ = os.Remove(f.Name())
	} else if err := os.Rename(f.Name(), body); err != nil {
		return m, fmt.Errorf("spooling %s: %w", body, err)
	}

	return m, saveJSON(s.metaPath(m.ID), m)
}

// attach records a delivery over an already-parked body: the sender
// contributes only its header. The meta is returned unsaved — the
// caller persists it only once the sender's opSent confirms the
// stream was well-formed.
func (s *spool) attach(src io.Reader, m spoolMeta) (spoolMeta, error) {
	fi, err := os.Stat(spoolBodyPath(s.dir, m.From, m.SHA, m.Plain))
	if err != nil {
		return m, fmt.Errorf("parked body vanished: %w", err)
	}

	hdr := make([]byte, headerLen)
	if _, err := io.ReadFull(src, hdr); err != nil {
		return m, fmt.Errorf("%w: header cut short", errCorrupt)
	}

	m.ID = newID()
	if m.At == 0 {
		m.At = time.Now().Unix()
	}

	m.Header = hdr
	m.Size = headerLen + fi.Size()

	return m, nil
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

// open returns the meta and backing file for id: the shared body for
// headerless entries, the entry's own blob otherwise.
func (s *spool) open(id string) (spoolMeta, *os.File, error) {
	m := spoolMeta{}
	if err := loadJSON(s.metaPath(id), &m); err != nil {
		return m, nil, fmt.Errorf("spool meta %s: %w", id, err)
	}

	path := s.blobPath(id)
	if m.Header != nil {
		path = spoolBodyPath(s.dir, m.From, m.SHA, m.Plain)
	}

	f, err := os.Open(path)
	if err != nil {
		return m, nil, fmt.Errorf("spool %s: %w", path, err)
	}

	return m, f, nil
}

// delete removes one entry; missing files are not an error. A shared
// body is reclaimed only when the deleted meta was its last.
func (s *spool) delete(id string) error {
	m := spoolMeta{}
	_ = loadJSON(s.metaPath(id), &m)

	if err := os.Remove(s.metaPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	body := ""
	if m.Header != nil {
		body = spoolBodyPath(s.dir, m.From, m.SHA, m.Plain)
	}

	if body == "" {
		err := os.Remove(s.blobPath(id))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}

		return nil
	}

	metas, err := s.items()
	if err != nil {
		return err // keep the body: leak beats deleting another target's delivery
	}

	for _, sm := range metas {
		if sm.Header != nil && sm.From == m.From && sm.SHA == m.SHA && sm.Plain == m.Plain {
			return nil
		}
	}

	err = os.Remove(body)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
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

// sweep deletes entries older than ttl, then reclaims bodies no
// meta references — a crash between a body's rename and its sidecar
// otherwise leaks the spool's biggest files.
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

	return s.sweepBodies()
}

// sweepBodies removes shared bodies nothing references.
func (s *spool) sweepBodies() error {
	metas, err := s.items()
	if err != nil {
		return err
	}

	live := make(map[string]bool, len(metas))
	for _, m := range metas {
		if m.Header != nil {
			live[filepath.Base(spoolBodyPath(s.dir, m.From, m.SHA, m.Plain))] = true
		}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".body") || live[e.Name()] {
			continue
		}

		_ = os.Remove(filepath.Join(s.dir, e.Name()))
	}

	return nil
}
