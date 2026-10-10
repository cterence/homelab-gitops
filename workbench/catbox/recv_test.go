package main

// The listener's receive path, driven over net.Pipe without tailcat.

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tailscale.com/types/key"
)

// The listener announces a direct send before any bytes move: the
// CLI log and the app's parser both read plain stderr lines.
func TestListenerAnnouncesDirectSender(t *testing.T) {
	nas := testPeerID("nas")

	_, c := testListener(t, nas)
	defer func() { _ = c.Close() }()

	capR, capW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stderr
	os.Stderr = capW

	if err := writeMsg(c, msg{Op: opSend, Target: "nas", From: "macbook-home", FileName: "hello.txt", Size: 5}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	os.Stderr = old
	_ = capW.Close()

	out, err := io.ReadAll(capR)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opReady || !m.OK {
		t.Fatalf("ready = %+v", m)
	}

	if !strings.Contains(string(out), "macbook-home is sending hello.txt directly") {
		t.Fatalf("announce = %q", out)
	}
}

func testListener(t *testing.T, id *peerID) (*listener, net.Conn) {
	t.Helper()
	lc := &listener{inbox: t.TempDir(), log: slog.New(slog.NewJSONHandler(io.Discard, nil)), id: id}

	c, s := net.Pipe()

	go func() {
		defer func() { _ = s.Close() }()

		lc.listenConn(s)
	}()

	return lc, c
}
func TestListenerReceivesDirect(t *testing.T) {
	nas := testPeerID("nas")

	lc, c := testListener(t, nas)
	defer func() { _ = c.Close() }()

	if err := writeMsg(c, msg{Op: opSend, Target: "nas", FileName: "hello.txt", Size: 5}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opReady || !m.OK {
		t.Fatalf("ready = %+v", m)
	}

	sender := key.NewNode()

	plainSize, err := sealStream(sender, nas.Key.Public(), c, bytes.NewReader([]byte("hello")), shaHexOf([]byte("hello")), 0)
	if err != nil {
		t.Fatal(err)
	}

	if plainSize != 5 {
		t.Fatalf("plainSize = %d", plainSize)
	}

	if err := writeMsg(c, msg{Op: opSent, SHA: shaHexOf([]byte("hello"))}); err != nil {
		t.Fatal(err)
	}

	m, err = readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opDone || !m.OK {
		t.Fatalf("done = %+v", m)
	}

	got, err := os.ReadFile(filepath.Join(lc.inbox, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
}

func TestListenerRefusesOtherTargets(t *testing.T) {
	nas := testPeerID("nas")

	_, c := testListener(t, nas)
	defer func() { _ = c.Close() }()

	if err := writeMsg(c, msg{Op: opSend, Target: "laptop", FileName: "x", Size: 1}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opReady || m.Err == "" {
		t.Fatalf("refusal = %+v", m)
	}
}

// A locked partial — a pull is already delivering this exact content —
// must be refused before any bytes move: the sender falls back to the
// stash instead of interleaving two receivers into corruption.
func TestListenerDeclinesBusyPartial(t *testing.T) {
	nas := testPeerID("nas")
	sha := shaHexOf([]byte("hello"))

	lc, c := testListener(t, nas)
	defer func() { _ = c.Close() }()

	release, err := lockPartial(lc.inbox, sha, 5)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	defer release()

	if err := writeMsg(c, msg{Op: opSend, Target: "nas", FileName: "hello.txt", Size: 5, SHA: sha}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	if m.OK || m.Err != busyRefusal {
		t.Fatalf("busy partial must be refused with %q, got %+v", busyRefusal, m)
	}

	if _, err := os.Stat(partialPath(lc.inbox, sha, 5)); !os.IsNotExist(err) {
		t.Fatalf("refused send must not touch the partial: %v", err)
	}
}

func TestListenerShaMismatch(t *testing.T) {
	nas := testPeerID("nas")

	lc, c := testListener(t, nas)
	defer func() { _ = c.Close() }()

	if err := writeMsg(c, msg{Op: opSend, Target: "nas", FileName: "bad.txt", Size: 5}); err != nil {
		t.Fatal(err)
	}

	if _, err := readMsg(c); err != nil {
		t.Fatal(err)
	}

	if _, err := sealStream(key.NewNode(), nas.Key.Public(), c, bytes.NewReader([]byte("hello")), shaHexOf([]byte("hello")), 0); err != nil {
		t.Fatal(err)
	}

	if err := writeMsg(c, msg{Op: opSent, SHA: "deadbeef"}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	if m.OK {
		t.Fatalf("sha mismatch should fail, got %+v", m)
	}

	entries, err := os.ReadDir(lc.inbox)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Fatalf("inbox should be empty: %v", entries)
	}
}

// A malformed SHA on a direct send must be refused before it keys a
// partial or names a path, not crash the listener.
func TestListenerRefusesBadSHA(t *testing.T) {
	nas := testPeerID("nas")

	for _, sha := range []string{"abc", strings.Repeat("a/", 32)} {
		lc, c := testListener(t, nas)
		if err := writeMsg(c, msg{Op: opSend, Target: "nas", FileName: "x", Size: 5, SHA: sha}); err != nil {
			t.Fatal(err)
		}

		m, err := readMsg(c)
		_ = c.Close()

		if err != nil {
			t.Fatal(err)
		}

		if m.OK || m.Err == "" {
			t.Fatalf("malformed SHA %q must be refused, got %+v", sha, m)
		}

		entries, err := os.ReadDir(lc.inbox)
		if err != nil || len(entries) != 0 {
			t.Fatalf("refused send must not write files: %v %v", err, entries)
		}
	}
}

// A sender streaming more than it claimed is cut off by the receive
// budget, not written to disk until the terminator.
func TestListenerBoundsOversizeSend(t *testing.T) {
	nas := testPeerID("nas")

	lc, c := testListener(t, nas)
	defer func() { _ = c.Close() }()

	if err := writeMsg(c, msg{Op: opSend, Target: "nas", FileName: "liar.txt", Size: 5}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opReady || !m.OK {
		t.Fatalf("ready = %+v", m)
	}

	payload := bytes.Repeat([]byte("x"), 100)

	go func() {
		_, _ = sealStream(key.NewNode(), nas.Key.Public(), c, bytes.NewReader(payload), shaHexOf(payload), 0)
	}()

	m, err = readMsg(c)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opDone || m.OK || m.Err == "" {
		t.Fatalf("oversize send must fail, got %+v", m)
	}

	// The refusal precedes the handler's deferred temp cleanup: poll.
	deadline := time.Now().Add(2 * time.Second)

	for {
		entries, err := os.ReadDir(lc.inbox)
		if err != nil {
			t.Fatal(err)
		}

		if len(entries) == 0 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("oversize send must not deliver: %v", entries)
		}

		time.Sleep(time.Millisecond)
	}
}
