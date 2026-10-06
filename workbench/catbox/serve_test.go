package main

// End-to-end protocol over net.Pipe: join, send, pull. No tailcat, no
// network: serveConn takes the peer key directly.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

func newTestStorer(t *testing.T) *storer {
	t.Helper()
	dir := t.TempDir()

	sp, err := openSpool(spoolDir(dir))
	if err != nil {
		t.Fatal(err)
	}

	return &storer{
		dir:   dir,
		log:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		max:   1 << 30,
		ttl:   24 * time.Hour,
		spool: sp,
	}
}

// dial wires one client session to serveConn over net.Pipe.
func dial(t *testing.T, st *storer, client *peerID) net.Conn {
	t.Helper()

	c, s := net.Pipe()

	go func() {
		defer func() { _ = s.Close() }()

		st.serveConn(s, client.DialKey.Public())
	}()

	return c
}

func testPeerID(name string) *peerID {
	return &peerID{Name: name, Key: key.NewNode(), DialKey: key.NewNode(), StorerAddr: "tcunused"}
}

func TestJoinSendPull(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the real config dir untouched

	ctx := context.Background()
	st := newTestStorer(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, ""); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("pick up milk"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = conn.Close()

	metas, err := st.spool.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 1 || metas[0].Target != "nas" || metas[0].From != "laptop" || metas[0].FileName != "notes.txt" {
		t.Fatalf("spool = %+v", metas)
	}

	if metas[0].SHA == "" {
		t.Fatal("meta missing plaintext SHA")
	}

	inbox := t.TempDir()

	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nas, inbox); err != nil {
		t.Fatalf("inbox: %v", err)
	}

	_ = conn.Close()

	got, err := os.ReadFile(filepath.Join(inbox, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, []byte("pick up milk")) {
		t.Fatalf("content = %q", got)
	}

	metas, err = st.spool.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 0 {
		t.Fatalf("spool not emptied: %+v", metas)
	}
}

func TestSendUnknownMemberRefreshesRoster(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStorer(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, ""); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	// Wipe the laptop's roster cache: the refresh path must find "nas".
	if err := os.Remove(rosterPath(peerConfigDir())); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src); err != nil {
		t.Fatalf("send with refresh: %v", err)
	}

	_ = conn.Close()

	if metas, _ := st.spool.items(); len(metas) != 1 {
		t.Fatalf("spool = %+v", metas)
	}
}

func TestSendToUnknownNameFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStorer(t)
	laptop := testPeerID("laptop")

	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, ""); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn = dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "ghost", src); err == nil {
		t.Fatal("send to unknown member should fail")
	}

	_ = conn.Close()
}

func TestJoinNameTaken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStorer(t)
	first := testPeerID("laptop")
	squatter := testPeerID("laptop")

	conn := dial(t, st, first)
	if err := joinReq(conn, first, ""); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	conn = dial(t, st, squatter)
	if err := joinReq(conn, squatter, ""); err == nil {
		t.Fatal("duplicate name should be rejected")
	}

	_ = conn.Close()
}

func TestRejoinUpdatesListenerAddr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStorer(t)
	laptop := testPeerID("laptop")

	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, ""); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	// Register a listener address, then clear it on shutdown.
	for _, addr := range []string{"tcregistered", ""} {
		conn = dial(t, st, laptop)
		if err := joinReq(conn, laptop, tailcat.Addr(addr)); err != nil {
			t.Fatalf("rejoin with %q: %v", addr, err)
		}

		_ = conn.Close()

		m, ok := memberByName(st.members(), "laptop")
		if !ok || string(m.Addr) != addr {
			t.Fatalf("after rejoin with %q: member = %+v", addr, m)
		}
	}
}

func TestClientStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStorer(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, ""); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	// Register laptop as listening, then check the status output.
	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, "tclistening"); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	// Park a file for laptop so the waiting line carries a byte count.
	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn = dial(t, st, nas)
	if err := clientSend(context.Background(), conn, nas, "laptop", src); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	var out bytes.Buffer

	conn = dial(t, st, laptop)
	if err := clientStatus(conn, &out, laptop); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	got := out.String()
	for _, want := range []string{"inbox: 1 waiting (5 B)", "members: 2", "nas\n", "laptop [you] (listening)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}

	if strings.Index(got, "laptop") > strings.Index(got, "nas") {
		t.Fatalf("members not sorted:\n%s", got)
	}
}

func TestSendToSelfFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStorer(t)
	laptop := testPeerID("laptop")

	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, ""); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Client-side guard: refuses before any network op.
	if err := clientSend(ctx, nil, laptop, "laptop", src); err == nil {
		t.Fatal("self-send should fail client-side")
	}

	// Server-side guard: a bare opSend to self is refused.
	conn = dial(t, st, laptop)
	if err := writeMsg(conn, msg{Op: opSend, Target: "laptop", FileName: "f.txt", Size: 1}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	if m.Op != opReady || m.OK || m.Err == "" {
		t.Fatalf("storer should refuse self-send, got %+v", m)
	}
}

func TestDepositDedup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStorer(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, ""); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("same bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		conn := dial(t, st, laptop)
		if err := clientSend(ctx, conn, laptop, "nas", src); err != nil {
			t.Fatalf("send: %v", err)
		}

		_ = conn.Close()
	}

	if metas, _ := st.spool.items(); len(metas) != 1 {
		t.Fatalf("identical deposits should collapse to one spool entry, got %d", len(metas))
	}
}

func TestNonMemberCannotSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStorer(t)
	stranger := testPeerID("stranger")

	conn := dial(t, st, stranger)
	defer func() { _ = conn.Close() }()

	if err := writeMsg(conn, msg{Op: opPending}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	if m.Err == "" {
		t.Fatalf("non-member should be refused, got %+v", m)
	}
}
