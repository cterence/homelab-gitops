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

	plainSize, sha, err := sealStream(sender, nas.Key.Public(), c, bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}

	if plainSize != 5 {
		t.Fatalf("plainSize = %d", plainSize)
	}

	if err := writeMsg(c, msg{Op: opSent, SHA: sha}); err != nil {
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

	if _, _, err := sealStream(key.NewNode(), nas.Key.Public(), c, bytes.NewReader([]byte("hello"))); err != nil {
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
