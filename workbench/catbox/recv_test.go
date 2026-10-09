package main

// The listener's receive path, driven over net.Pipe without tailcat.

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"tailscale.com/types/key"
)

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
// storer instead of interleaving two receivers into corruption.
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
