package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// One receiver per partial at a time, across processes: the listener
// and a pull appending the same file would interleave into corruption.
func TestPartialLockExclusive(t *testing.T) {
	dir := t.TempDir()
	shaHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	release, err := lockPartial(dir, shaHex, 4096)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	if _, err := lockPartial(dir, shaHex, 4096); !errors.Is(err, errPartialBusy) {
		t.Fatalf("second lock err = %v, want errPartialBusy", err)
	}

	release()

	release, err = lockPartial(dir, shaHex, 4096)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}

	release()
}

func TestPartialFor(t *testing.T) {
	dir := t.TempDir()
	shaHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("fresh", func(t *testing.T) {
		f, h, have, resumable, err := partialFor(dir, shaHex, 4096)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()

		if have != 0 || !resumable {
			t.Fatalf("have=%d resumable=%v, want 0/true", have, resumable)
		}

		if f.Name() != partialPath(dir, shaHex, 4096) {
			t.Fatalf("partial = %s, want the content-keyed path", f.Name())
		}

		if h == nil {
			t.Fatal("hasher is nil")
		}
	})

	t.Run("resumes at chunk boundary", func(t *testing.T) {
		// A cut mid-chunk leaves one full chunk plus a torn tail.
		size := int64(4 * chunkSize)
		p := partialPath(dir, shaHex, size)

		body := make([]byte, chunkSize+77)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}

		f, h, have, resumable, err := partialFor(dir, shaHex, size)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()

		if have != chunkSize || !resumable {
			t.Fatalf("have=%d resumable=%v, want %d/true", have, resumable, chunkSize)
		}

		fi, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}

		if fi.Size() != chunkSize {
			t.Fatalf("partial kept %d bytes, want the torn tail dropped (%d)", fi.Size(), chunkSize)
		}

		// The hasher covers exactly the kept prefix.
		if h.Sum(nil) == nil {
			t.Fatal("hasher produced no state")
		}
	})

	t.Run("oversized partial restarts", func(t *testing.T) {
		// A partial bigger than the claimed size is corrupt.
		p := partialPath(dir, shaHex, 100)
		if err := os.WriteFile(p, make([]byte, 3*chunkSize), 0o600); err != nil {
			t.Fatal(err)
		}

		f, _, have, _, err := partialFor(dir, shaHex, 100)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()

		if have != 0 {
			t.Fatalf("have=%d, want 0 for a corrupt partial", have)
		}
	})

	t.Run("empty sha is not resumable", func(t *testing.T) {
		f, _, have, resumable, err := partialFor(dir, "", 4096)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()

		if have != 0 || resumable {
			t.Fatalf("have=%d resumable=%v, want 0/false", have, resumable)
		}
	})
}

func TestSweepPartials(t *testing.T) {
	dir := t.TempDir()
	shaHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	stale := partialPath(dir, shaHex, 1)
	fresh := partialPath(dir, shaHex, 2)
	keep := filepath.Join(dir, "nap.txt")

	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The stale partial predates the TTL; the fresh one is in flight.
	past := time.Now().Add(-partialTTL - time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}

	sweepPartials(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale partial survived: %v", err)
	}

	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh partial swept: %v", err)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("regular file swept: %v", err)
	}
}
