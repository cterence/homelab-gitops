package main

// Resume state: interrupted transfers keep their received bytes in
// content-keyed partials (`.part-<sha12>-<size>`), so a retry — direct
// or via the storer — continues at its chunk boundary instead of
// starting over. The partial's mtime is the liveness signal: bytes
// flowing refresh it, and a stale partial is swept by TTL.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// errPartialBusy marks a transfer whose partial another receiver is
// already writing: the listener and a pull appending the same file
// would interleave plaintext into corruption, so the second declines.
var errPartialBusy = errors.New("another receive is already writing this partial")

// busyRefusal is the listener's wire form of errPartialBusy: the
// sender stops its direct retries and falls back to the storer,
// where the identical item is already parked.
const busyRefusal = "partial busy"

// lockPartial takes an exclusive advisory lock on a transfer's
// partial: one receiver per content at a time, across processes (the
// listener and a pull are separate children). The lock dies with its
// holder; the lock file sweeps with the partials by TTL.
func lockPartial(dir, shaHex string, size int64) (release func(), err error) {
	if shaHex == "" {
		return func() {}, nil // plain temps are private by name
	}

	f, err := os.OpenFile(partialPath(dir, shaHex, size)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, nil // no lock possible: proceed unlocked
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		return nil, errPartialBusy
	}

	return func() { _ = f.Close() }, nil
}

// partialTTL bounds how long a partial waits for its sender: the
// sender's retry window is minutes, so an hour covers an unlock
// resume — longer is storage clogging for nothing.
const partialTTL = 1 * time.Hour

// fileSHA256 streams one file through SHA-256: known before sealing,
// it keys the deterministic file secret and the receiver's partial.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer func() { _ = f.Close() }()

	h := sha256.New()

	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// partialPath names a transfer's resume file: content-keyed, so a
// retry over any path finds it.
func partialPath(dir, shaHex string, size int64) string {
	return filepath.Join(dir, fmt.Sprintf(".part-%s-%d", shaHex[:12], size))
}

// partialFor opens the resume target for a transfer of shaHex/size
// into dir: an existing partial continues — truncated to its last
// chunk boundary, its kept prefix re-hashed into h — and a fresh one
// starts empty. resumable reports whether the target survives a
// failed attempt (only content-keyed partials do).
func partialFor(dir, shaHex string, size int64) (f *os.File, h hash.Hash, have int64, resumable bool, err error) {
	if shaHex == "" {
		f, err = os.CreateTemp(dir, ".part-*")

		return f, sha256.New(), 0, false, err
	}

	f, err = os.OpenFile(partialPath(dir, shaHex, size), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, 0, false, err
	}

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return nil, nil, 0, false, err
	}

	// Drop the torn tail: a cut mid-chunk leaves unverifiable bytes.
	// A partial bigger than the claimed size is corrupt: restart.
	have = fi.Size() / chunkSize * chunkSize
	if have > size {
		have = 0
	}

	if err := f.Truncate(have); err != nil {
		_ = f.Close()

		return nil, nil, 0, false, err
	}

	h = sha256.New()

	if have > 0 {
		pf, err := os.Open(f.Name())
		if err != nil {
			_ = f.Close()

			return nil, nil, 0, false, err
		}

		_, err = io.CopyN(h, pf, have)
		_ = pf.Close()

		if err != nil {
			_ = f.Close()

			return nil, nil, 0, false, err
		}
	}

	return f, h, have, true, nil
}

// sweepPartials deletes .part-* files older than the TTL: their
// transfer is not coming back. In-flight partials are untouched —
// every write refreshes their mtime.
func sweepPartials(dir string) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, de := range des {
		if !strings.HasPrefix(de.Name(), ".part-") {
			continue
		}

		fi, err := de.Info()
		if err != nil || time.Since(fi.ModTime()) < partialTTL {
			continue
		}

		_ = os.Remove(filepath.Join(dir, de.Name()))
	}
}
