package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"tailscale.com/types/key"
)

// A fake clock the reader consults instead of time.Now.
type fakeClock struct{ t time.Time }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newProgress(t *testing.T, total int64, r io.Reader) (*progressReader, *bytes.Buffer, *fakeClock) {
	t.Helper()

	clock := &fakeClock{t: time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC)}

	var out bytes.Buffer

	return &progressReader{
		r:     r,
		total: total,
		label: "sending",
		every: time.Second,
		w:     &out,
		now:   func() time.Time { return clock.t },
	}, &out, clock
}

func readN(t *testing.T, r io.Reader, n int) {
	t.Helper()

	if _, err := io.ReadFull(r, make([]byte, n)); err != nil {
		t.Fatal(err)
	}
}

func TestProgressReaderEmitsPerInterval(t *testing.T) {
	r, out, clock := newProgress(t, 1000, strings.NewReader(strings.Repeat("a", 1000)))

	readN(t, r, 100) // t0: counters start

	clock.advance(time.Second)
	readN(t, r, 200) // t1: 300 total, 200 B/s

	clock.advance(time.Second)
	readN(t, r, 300) // t2: 600 total, 300 B/s

	got := out.String()
	for _, want := range []string{
		"sending 300 B / 1000 B (30%), 200 B/s",
		"sending 600 B / 1000 B (60%), 300 B/s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestProgressReaderUnknownTotal(t *testing.T) {
	r, out, clock := newProgress(t, 0, strings.NewReader(strings.Repeat("a", 500)))

	readN(t, r, 100)

	clock.advance(time.Second)
	readN(t, r, 100)

	if want := "sending 200 B (100 B/s)"; !strings.Contains(out.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, out.String())
	}
}

func TestProgressReaderNoEmitBeforeInterval(t *testing.T) {
	r, out, _ := newProgress(t, 1000, strings.NewReader(strings.Repeat("a", 1000)))

	readN(t, r, 100)
	readN(t, r, 100)

	if out.Len() != 0 {
		t.Fatalf("emitted before the interval elapsed: %q", out.String())
	}
}

// The live path provider rides inside the tick line's parens.
func TestProgressReaderLivePath(t *testing.T) {
	r, out, clock := newProgress(t, 1000, strings.NewReader(strings.Repeat("a", 400)))

	r.path = func() string { return "direct 192.0.2.1:41414" }

	readN(t, r, 100) // t0: counters start

	clock.advance(time.Second)
	readN(t, r, 200) // t1: 300 total

	if want := "sending 300 B / 1000 B (30%), 200 B/s, path: direct 192.0.2.1:41414"; !strings.Contains(out.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, out.String())
	}
}

// A resumed transfer's line shows the full-file position, not the wire
// count: the counter starts at the resume offset.
func TestProgressReaderResumeOffset(t *testing.T) {
	r, out, clock := newProgress(t, 1000, strings.NewReader(strings.Repeat("a", 300)))

	r.offset = 700 // already on disk before this attempt

	readN(t, r, 100) // t0: counters start

	clock.advance(time.Second)
	readN(t, r, 100) // 200 wire bytes: 900 of the file

	clock.advance(time.Second)
	readN(t, r, 100) // 300 wire bytes: the whole file

	got := out.String()
	for _, want := range []string{
		"sending 900 B / 1000 B (90%",
		"sending 1000 B / 1000 B (100%",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

// The data path must not notice the progress wrapper: seal 64 MiB with
// and without it and compare throughputs.
func benchmarkSealStream(b *testing.B, wrap bool) {
	sender := key.NewNode()
	recipient := key.NewNode()

	src := make([]byte, 64<<20)

	b.SetBytes(int64(len(src)))

	for b.Loop() {
		var r io.Reader = bytes.NewReader(src)

		if wrap {
			r = &progressReader{r: r, total: int64(len(src)), label: "sending", every: time.Second, w: io.Discard}
		}

		if _, err := sealStream(sender, recipient.Public(), io.Discard, r, "test", 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSealStream(b *testing.B)        { benchmarkSealStream(b, false) }
func BenchmarkSealStreamWrapped(b *testing.B) { benchmarkSealStream(b, true) }

func TestProgressReaderRewritesOnTerminal(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC)}

	var out bytes.Buffer

	r := &progressReader{
		r:       strings.NewReader(strings.Repeat("a", 500)),
		total:   500,
		label:   "sending",
		every:   time.Second,
		rewrite: true,
		w:       &out,
		now:     func() time.Time { return clock.t },
	}

	readN(t, r, 100) // counters start

	clock.advance(time.Second)
	readN(t, r, 100) // emit, redrawn

	got := out.String()
	if !strings.Contains(got, "\r  sending [") {
		t.Fatalf("terminal mode must draw the bar:\n%q", got)
	}

	if !strings.Contains(got, "] 200 B / 500 B (40%), 100 B/s") {
		t.Fatalf("barred progress line wrong:\n%q", got)
	}

	if strings.Contains(got, "eta") {
		t.Fatalf("the line must not carry an eta:\n%q", got)
	}

	if strings.Contains(got, "\n") {
		t.Fatalf("terminal mode must not emit newlines:\n%q", got)
	}

	readN(t, r, 300) // the last bytes...

	if _, err := r.Read(make([]byte, 1)); err != io.EOF { // ...then EOF erases the line
		t.Fatal(err)
	}

	if !strings.HasSuffix(out.String(), "\r\x1b[2K") {
		t.Fatalf("EOF must erase the progress line:\n%q", out.String())
	}
}

// The lock is mutual exclusion: a second command waits until the
// holder releases, and a canceled wait never runs.
func TestLockPeer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()

	release, err := lockPeer(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan func(), 1)

	go func() {
		r, err := lockPeer(ctx)
		if err == nil {
			got <- r
		}
	}()

	select {
	case <-got:
		t.Fatal("the lock was taken while the holder lives")
	case <-time.After(600 * time.Millisecond):
	}

	release()

	select {
	case rel := <-got:
		rel()
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never took the released lock")
	}

	// A canceled wait never takes the lock.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := lockPeer(cctx); err == nil {
		t.Fatal("a canceled wait must not take the lock")
	}

	// With everyone gone, a fresh command takes it at once.
	again, err := lockPeer(ctx)
	if err != nil {
		t.Fatal(err)
	}

	again()
}

// A waiting command says so: a silent stall looks like a hang.
func TestLockPeerWaitingLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release, err := lockPeer(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	defer release()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	saved := os.Stderr
	os.Stderr = w

	defer func() { os.Stderr = saved }()

	done := make(chan error, 1)

	go func() {
		rel, err := lockPeer(ctx)
		if err == nil {
			rel()
		}

		done <- err
	}()

	time.Sleep(400 * time.Millisecond) // the waiter logs on its first poll

	_ = w.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(got), "another catbox command is running, waiting") {
		t.Fatalf("no waiting note: %q", got)
	}

	cancel()

	if err := <-done; err == nil {
		t.Fatal("the canceled waiter must give up")
	}
}
