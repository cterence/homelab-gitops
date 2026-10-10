package main

// End-to-end protocol over net.Pipe: join, send, pull. No tailcat, no
// network: serveConn takes the peer key directly.

import (
	"bytes"
	"context"
	"encoding/json"
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

// mustSHA hashes a file the way the send command does before sealing.
func mustSHA(t *testing.T, path string) string {
	t.Helper()

	sha, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}

	return sha
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func newTestStash(t *testing.T) *stash {
	t.Helper()
	dir := t.TempDir()

	// The stash identity: stash-host commands (admin, remove --data)
	// refuse a dir without one.
	if _, _, err := loadStashIdentity(context.Background(), dir, 1); err != nil {
		t.Fatal(err)
	}

	sp, err := openSpool(spoolDir(dir), testLogger())
	if err != nil {
		t.Fatal(err)
	}

	return &stash{
		dir:   dir,
		log:   testLogger(),
		max:   1 << 30,
		ttl:   24 * time.Hour,
		spool: sp,
	}
}

// dial wires one client session to serveConn over net.Pipe.
func dial(t *testing.T, st *stash, client *peerID) net.Conn {
	t.Helper()

	c, s := net.Pipe()

	go func() {
		defer func() { _ = s.Close() }()

		st.serveConn(s, client.DialKey.Public())
	}()

	return c
}

func testPeerID(name string) *peerID {
	return &peerID{Name: name, Key: key.NewNode(), DialKey: key.NewNode(), StashAddr: "tcunused"}
}

// dialMember wires a session for an already-joined member by its
// roster dial key: tests hold the *peerID only for members they dial as.
func dialMember(t *testing.T, st *stash, k key.NodePublic) net.Conn {
	t.Helper()

	c, s := net.Pipe()

	go func() {
		defer func() { _ = s.Close() }()

		st.serveConn(s, k)
	}()

	return c
}

// testInvite mints a join code from the roster's first member (the
// first join bootstraps as admin); empty when the roster is empty —
// the first join needs no code.
func testInvite(t *testing.T, st *stash) string {
	t.Helper()

	members := st.members()
	if len(members) == 0 {
		return ""
	}

	conn := dialMember(t, st, members[0].DialKey)
	defer func() { _ = conn.Close() }()

	code, err := clientInvite(conn)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	return code
}

func TestJoinSendPull(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the real config dir untouched

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("pick up milk"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
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
	if err := clientInbox(ctx, conn, nil, nas, inbox, nil); err != nil {
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
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
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
	if err := clientSend(ctx, conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
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
	st := newTestStash(t)
	laptop := testPeerID("laptop")

	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, "", testInvite(t, st)); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn = dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "ghost", src, mustSHA(t, src)); err == nil {
		t.Fatal("send to unknown member should fail")
	}

	_ = conn.Close()
}

func TestJoinNameTaken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStash(t)
	first := testPeerID("laptop")
	squatter := testPeerID("laptop")

	conn := dial(t, st, first)
	if err := joinReq(conn, first, "", testInvite(t, st)); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	conn = dial(t, st, squatter)
	if err := joinReq(conn, squatter, "", ""); err == nil {
		t.Fatal("duplicate name should be rejected")
	}

	_ = conn.Close()
}

func TestRejoinUpdatesListenerAddr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStash(t)
	laptop := testPeerID("laptop")

	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, "", testInvite(t, st)); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	// Register a listener address, then clear it on shutdown.
	for _, addr := range []string{"tcregistered", ""} {
		conn = dial(t, st, laptop)
		if err := joinReq(conn, laptop, tailcat.Addr(addr), ""); err != nil {
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

	// Direct retries would crawl through the real retry window on the
	// bogus listener address: this test only exercises the deposit.
	defer func(w time.Duration) { directRetryWindow = w }(directRetryWindow)

	directRetryWindow = 0

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	// Register laptop as listening, then check the status output.
	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, "tclistening", ""); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	// Park a file for laptop so the waiting line carries a byte count.
	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn = dial(t, st, nas)
	if err := clientSend(context.Background(), conn, nas, "laptop", src, mustSHA(t, src)); err != nil {
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
	for _, want := range []string{
		"inbox: 1 waiting (5 B)",
		"  f.txt from nas (5 B), id ",
		"members: 2",
		"laptop [admin] [you] (listening)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}

	membersAt := strings.Index(got, "members: 2")
	laptopAt := strings.Index(got, "laptop [admin] [you] (listening)")

	nasAt := strings.Index(got, "  nas\n")
	if membersAt < 0 || laptopAt < 0 || nasAt < 0 || (membersAt >= laptopAt || laptopAt >= nasAt) {
		t.Fatalf("members not sorted:\n%s", got)
	}
}

func TestSendToSelfFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")

	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, "", testInvite(t, st)); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Client-side guard: refuses before any network op.
	if err := clientSend(ctx, nil, laptop, "laptop", src, mustSHA(t, src)); err == nil {
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
		t.Fatalf("stash should refuse self-send, got %+v", m)
	}
}

func TestDepositDedup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
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
		if err := clientSend(ctx, conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
			t.Fatalf("send: %v", err)
		}

		_ = conn.Close()
	}

	if metas, _ := st.spool.items(); len(metas) != 1 {
		t.Fatalf("identical deposits should collapse to one spool entry, got %d", len(metas))
	}
}

func TestClientStatusJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Direct retries would crawl through the real retry window on the
	// bogus listener address: this test only exercises the deposit.
	defer func(w time.Duration) { directRetryWindow = w }(directRetryWindow)

	directRetryWindow = 0

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, nas)
	if err := clientSend(context.Background(), conn, nas, "laptop", src, mustSHA(t, src)); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	var out bytes.Buffer

	conn = dial(t, st, laptop)
	if err := clientStatusJSON(conn, &out, laptop); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	var s struct {
		Name    string `json:"name"`
		Waiting []item `json:"waiting"`
		Members []struct {
			Name string `json:"name"`
		} `json:"members"`
	}
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		t.Fatalf("parsing %s: %v", out.String(), err)
	}

	if s.Name != "laptop" {
		t.Fatalf("name = %q", s.Name)
	}

	if len(s.Waiting) != 1 || s.Waiting[0].FileName != "f.txt" || s.Waiting[0].Plain != 5 {
		t.Fatalf("waiting = %+v", s.Waiting)
	}

	if len(s.Members) != 2 || s.Members[0].Name != "laptop" || s.Members[1].Name != "nas" {
		t.Fatalf("members = %+v", s.Members)
	}
}

func TestStatusJSONEmptyWaiting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStash(t)
	laptop := testPeerID("laptop")

	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, "", testInvite(t, st)); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	var out bytes.Buffer

	conn = dial(t, st, laptop)
	if err := clientStatusJSON(conn, &out, laptop); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	got := out.String()
	if strings.Contains(got, "null") {
		t.Fatalf("empty waiting must marshal as [], got: %s", got)
	}

	if !strings.Contains(got, `"waiting":[]`) {
		t.Fatalf("missing empty waiting array: %s", got)
	}
}

func TestPeerNameValidated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name    string
		wantErr string
	}{
		{"", "run join with --name"},
		{"My Laptop", "invalid member name"},
		{"laptop", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := loadPeerID(tt.name, "tcx")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("valid name rejected: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNonMemberCannotSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newTestStash(t)
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

func TestDismissDestinedFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the real config dir untouched

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "spam.txt")
	if err := os.WriteFile(src, []byte("spam"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := clientSend(context.Background(), conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = conn.Close()

	metas, err := st.spool.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 1 {
		t.Fatalf("spool = %+v", metas)
	}

	conn = dial(t, st, nas)
	if err := clientDismiss(conn, metas[0].ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	_ = conn.Close()

	metas, err = st.spool.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 0 {
		t.Fatalf("spool not emptied: %+v", metas)
	}
}

func TestDismissOnlyOwnItems(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the real config dir untouched

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "spam.txt")
	if err := os.WriteFile(src, []byte("spam"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := clientSend(context.Background(), conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = conn.Close()

	metas, err := st.spool.items()
	if err != nil {
		t.Fatal(err)
	}

	// The sender is not the recipient: the item is not theirs to drop.
	conn = dial(t, st, laptop)
	if err := clientDismiss(conn, metas[0].ID); err == nil {
		t.Fatal("dismiss by non-target should fail")
	}

	_ = conn.Close()

	metas, err = st.spool.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 1 {
		t.Fatalf("someone else's dismiss deleted the item: %+v", metas)
	}
}

func TestRenameRetargetsSpool(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the real config dir untouched

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	// Park a file for nas before renaming it.
	src := filepath.Join(t.TempDir(), "park.txt")
	if err := os.WriteFile(src, []byte("parked"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := clientSend(context.Background(), conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = conn.Close()

	// Rename: same dial key, new name.
	nas.Name = "nas2"

	conn = dial(t, st, nas)
	if err := joinReq(conn, nas, "", ""); err != nil {
		t.Fatalf("rename: %v", err)
	}

	_ = conn.Close()

	// The old name is gone from the roster.
	conn = dial(t, st, laptop)
	if err := clientSend(context.Background(), conn, laptop, "nas", src, mustSHA(t, src)); err == nil {
		t.Fatal("send to the old name should fail")
	}

	_ = conn.Close()

	// The parked file followed the rename.
	inbox := t.TempDir()

	conn = dial(t, st, nas)
	if err := clientInbox(context.Background(), conn, nil, nas, inbox, nil); err != nil {
		t.Fatalf("inbox: %v", err)
	}

	_ = conn.Close()

	got, err := os.ReadFile(filepath.Join(inbox, "park.txt"))
	if err != nil || string(got) != "parked" {
		t.Fatalf("spool not retargeted by rename: %v %q", err, got)
	}
}

func TestRenameToTakenName(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the real config dir untouched

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	// nas may not steal laptop's name.
	nas.Name = "laptop"

	conn := dial(t, st, nas)
	if err := joinReq(conn, nas, "", ""); err == nil {
		t.Fatal("rename to a taken name should fail")
	}

	_ = conn.Close()

	// nas is still nas.
	if _, ok := memberByName(st.members(), "nas"); !ok {
		t.Fatal("failed rename dropped the member")
	}
}

// TestDepositResumesAfterInterruption pins the sender-side resume: an
// interrupted deposit keeps a partial at the stash, the retry is
// advertised its chunk boundary, and the resumed deposit's frames line
// up with the kept prefix.
func TestDepositResumesAfterInterruption(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	payload := bytes.Repeat([]byte("catbox deposit resume\n"), 200000/22+1)[:200000]

	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	sha := mustSHA(t, src)

	// First attempt: the deposit dies mid-stream after two whole
	// chunks, torn tail and all.
	var sealed bytes.Buffer
	if _, err := sealStream(laptop.Key, nas.Key.Public(), &sealed, bytes.NewReader(payload), sha, 0); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := writeMsg(conn, msg{Op: opSend, Target: "nas", FileName: "big.bin", Size: int64(len(payload)), SHA: sha}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opReady || !m.OK {
		t.Fatalf("first opReady = %+v", m)
	}

	if _, err := conn.Write(sealed.Bytes()[:headerLen+2*chunkFrameLen+10]); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close() // mid-deposit disconnect

	if metas, _ := st.spool.items(); len(metas) != 0 {
		t.Fatalf("interrupted deposit must not appear as a spool entry: %+v", metas)
	}

	// Retry: the stash advertises the partial's chunk boundary, the
	// torn tail dropped.
	conn = dial(t, st, laptop)
	if err := writeMsg(conn, msg{Op: opSend, Target: "nas", FileName: "big.bin", Size: int64(len(payload)), SHA: sha}); err != nil {
		t.Fatal(err)
	}

	m, err = readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	_ = conn.Close() // abandoned again; the partial must survive

	if m.Op != opReady || !m.OK {
		t.Fatalf("retry opReady = %+v", m)
	}

	if m.Have != 2*chunkSize {
		t.Fatalf("retry opReady Have = %d, want %d", m.Have, 2*chunkSize)
	}

	// Third attempt through the real client: it resumes and completes.
	conn = dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, sha); err != nil {
		t.Fatalf("resumed send: %v", err)
	}

	_ = conn.Close()

	// The pull delivers the whole file: frames from both attempts must
	// decrypt as one stream.
	inbox := t.TempDir()

	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, nil); err != nil {
		t.Fatalf("inbox: %v", err)
	}

	_ = conn.Close()

	got, err := os.ReadFile(filepath.Join(inbox, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, payload) {
		t.Fatalf("resumed deposit delivered %d bytes, want %d", len(got), len(payload))
	}

	if metas, _ := st.spool.items(); len(metas) != 0 {
		t.Fatalf("spool not emptied after ack: %+v", metas)
	}
}

// TestDepositResumesNearCap pins the cap accounting: a resumed
// deposit's partial is already inside usage, so only the missing
// bytes count — a near-cap retry is not refused for space it holds.
func TestDepositResumesNearCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	// Room for the two-chunk partial plus the missing bytes, but not
	// for the whole file counted again.
	st.max = int64(headerLen+2*chunkFrameLen+10) + 100000

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	payload := bytes.Repeat([]byte("catbox near cap\n"), 200000/16+1)[:200000]

	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	sha := mustSHA(t, src)

	var sealed bytes.Buffer
	if _, err := sealStream(laptop.Key, nas.Key.Public(), &sealed, bytes.NewReader(payload), sha, 0); err != nil {
		t.Fatal(err)
	}

	// First attempt dies mid-stream after two whole chunks.
	conn := dial(t, st, laptop)
	if err := writeMsg(conn, msg{Op: opSend, Target: "nas", FileName: "big.bin", Size: int64(len(payload)), SHA: sha}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opReady || !m.OK {
		t.Fatalf("first opReady = %+v", m)
	}

	if _, err := conn.Write(sealed.Bytes()[:headerLen+2*chunkFrameLen+10]); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close() // mid-deposit disconnect: the partial survives

	// Retry under the tightened cap: must be admitted with resume.
	conn = dial(t, st, laptop)
	if err := writeMsg(conn, msg{Op: opSend, Target: "nas", FileName: "big.bin", Size: int64(len(payload)), SHA: sha}); err != nil {
		t.Fatal(err)
	}

	m, err = readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	if m.Op != opReady || !m.OK || m.Have != 2*chunkSize {
		t.Fatalf("retry opReady = %+v, want OK with Have=%d", m, 2*chunkSize)
	}

	// The resumed deposit completes within the remaining budget.
	conn = dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, sha); err != nil {
		t.Fatalf("resumed send: %v", err)
	}

	_ = conn.Close()

	inbox := t.TempDir()

	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, nil); err != nil {
		t.Fatalf("inbox: %v", err)
	}

	_ = conn.Close()

	got, err := os.ReadFile(filepath.Join(inbox, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, payload) {
		t.Fatalf("near-cap resumed deposit delivered %d bytes, want %d", len(got), len(payload))
	}
}

// The pull announces each file before the bytes, like the direct
// path: the app's live row names the file in flight.
func TestPullAnnouncesFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	sha := mustSHA(t, src)

	conn := dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, sha); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = conn.Close()

	capR, capW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stderr
	os.Stderr = capW

	inbox := t.TempDir()

	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, nil); err != nil {
		t.Fatalf("inbox: %v", err)
	}

	_ = conn.Close()

	os.Stderr = old
	_ = capW.Close()

	out, err := io.ReadAll(capR)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(out), "receiving hello.txt from laptop") {
		t.Fatalf("announce = %q", out)
	}
}

// An imperative cancel — Ctrl-C, the app's cancel button — must not
// leave a partial behind: the pull dies by ctx, its partial with it.
func TestPullCancelSweepsPartial(t *testing.T) {
	nas := testPeerID("nas")
	dir := t.TempDir()
	it := item{ID: "x", FileName: "f.bin", From: "laptop", SHA: strings.Repeat("a", 64), Plain: 4096, Size: 5000}

	c, s := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // imperative from the first byte

	go func() {
		defer func() { _ = s.Close() }()

		if m, err := readMsg(s); err != nil || m.Op != opFetch {
			return
		}

		_ = writeMsg(s, msg{Op: opFile, OK: true, FileName: it.FileName, From: it.From, Size: 100, Have: 0})
	}()

	if err := clientFetch(ctx, c, nil, nas, it, dir); err == nil {
		t.Fatal("canceled fetch: want error")
	}

	_ = c.Close()

	if _, err := os.Stat(partialPath(dir, it.SHA, it.Plain)); !os.IsNotExist(err) {
		t.Fatalf("canceled pull left its partial: %v", err)
	}
}

// TestPullSelectedItems pins the id-filtered pull: recv with ids
// delivers only those items, the rest stay parked, and an unknown id
// is an error.
func TestPullSelectedItems(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	type parked struct {
		id   string
		path string
	}

	var want []parked

	for _, name := range []string{"one.txt", "two.txt"} {
		src := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(src, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}

		conn := dial(t, st, laptop)
		if err := clientSend(ctx, conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
			t.Fatalf("send %s: %v", name, err)
		}

		_ = conn.Close()

		metas, err := st.spool.items()
		if err != nil {
			t.Fatal(err)
		}

		// Same-second deposits tie in the spool order: match by name.
		var id string

		for _, m := range metas {
			if m.FileName == name {
				id = m.ID
			}
		}

		if id == "" {
			t.Fatalf("%s never parked: %+v", name, metas)
		}

		want = append(want, parked{id: id, path: src})
	}

	inbox := t.TempDir()

	// Pull only the second item.
	conn := dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, []string{want[1].id}); err != nil {
		t.Fatalf("selective pull: %v", err)
	}

	_ = conn.Close()

	if _, err := os.Stat(filepath.Join(inbox, "two.txt")); err != nil {
		t.Fatalf("selected item not delivered: %v", err)
	}

	if _, err := os.Stat(filepath.Join(inbox, "one.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected item must stay parked: %v", err)
	}

	// An unknown id is an error, not a silent skip.
	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, []string{"bogus"}); err == nil || !strings.Contains(err.Error(), "no such pending item") {
		t.Fatalf("unknown id err = %v, want no such pending item", err)
	}

	_ = conn.Close()

	// Everything else still delivers when no ids are given.
	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, nil); err != nil {
		t.Fatalf("pull all: %v", err)
	}

	_ = conn.Close()

	if _, err := os.Stat(filepath.Join(inbox, "one.txt")); err != nil {
		t.Fatalf("remaining item not delivered: %v", err)
	}

	if metas, _ := st.spool.items(); len(metas) != 0 {
		t.Fatalf("spool not emptied: %+v", metas)
	}
}

// TestPullSkipsBusyPartial pins the pull side of the single-writer
// rule: a partial locked by a direct receive makes the pull skip that
// item without failing the run, and it delivers once freed.
func TestPullSkipsBusyPartial(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "busy.txt")
	if err := os.WriteFile(src, []byte("busy bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	sha := mustSHA(t, src)

	conn := dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, sha); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = conn.Close()

	inbox := t.TempDir()

	release, err := lockPartial(inbox, sha, int64(len("busy bytes")))
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, nil); err != nil {
		t.Fatalf("a busy partial must be skipped, not failed: %v", err)
	}

	_ = conn.Close()

	if metas, _ := st.spool.items(); len(metas) != 1 {
		t.Fatalf("skipped item must stay parked: %+v", metas)
	}

	release()

	conn = dial(t, st, nas)
	if err := clientInbox(ctx, conn, nil, nas, inbox, nil); err != nil {
		t.Fatalf("inbox after release: %v", err)
	}

	_ = conn.Close()

	got, err := os.ReadFile(filepath.Join(inbox, "busy.txt"))
	if err != nil || string(got) != "busy bytes" {
		t.Fatalf("deliver after release: %v %q", err, got)
	}

	if metas, _ := st.spool.items(); len(metas) != 0 {
		t.Fatalf("spool not emptied: %+v", metas)
	}
}

// TestDepositIdempotent pins the retried-deposit short-circuit: when
// an identical file is already parked for the target, the stash
// answers Have=Size, the sender sends no bytes, and the spool keeps
// exactly one entry.
func TestDepositIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("same bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	sha := mustSHA(t, src)

	// First send parks the file.
	conn := dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, sha); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = conn.Close()

	// A retried opSend is told the file is already parked.
	conn = dial(t, st, laptop)
	if err := writeMsg(conn, msg{Op: opSend, Target: "nas", FileName: "f.txt", Size: 10, SHA: sha}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	if m.Op != opReady || !m.OK || m.Have != 10 {
		t.Fatalf("retry opReady = %+v, want Have = full size", m)
	}

	if err := writeMsg(conn, msg{Op: opSent, SHA: sha}); err != nil {
		t.Fatal(err)
	}

	if m, err = readMsg(conn); err != nil || m.Op != opDone || !m.OK {
		t.Fatalf("retry done = %+v, err %v", m, err)
	}

	_ = conn.Close()

	// The real client takes the short path too, and the spool still
	// holds exactly one entry.
	conn = dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, sha); err != nil {
		t.Fatalf("idempotent resend: %v", err)
	}

	_ = conn.Close()

	metas, err := st.spool.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 1 {
		t.Fatalf("idempotent deposit duplicated the item: %+v", metas)
	}
}

// TestSendCachedRosterFirst: a cached listening address is dialed
// before the stash is contacted at all — and once both the target
// and the stash are unreachable, the send fails without a deposit.
func TestSendCachedRosterFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// The bogus listener address must fail fast, not crawl the retry
	// window.
	defer func(w time.Duration) { directRetryWindow = w }(directRetryWindow)

	directRetryWindow = 0

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	// nas "listens" on a bogus address; both the stash roster and
	// the shared cache carry it.
	conn := dial(t, st, nas)
	if err := joinReq(conn, nas, "tcbogus", ""); err != nil {
		t.Fatalf("listener rejoin: %v", err)
	}

	_ = conn.Close()

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Watch stderr: the direct attempt must run before the stash is
	// ever dialed.
	capR, capW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stderr
	os.Stderr = capW

	// rwc nil and an undialable stash address: the cache-first
	// direct attempt, then the send dies on the stash dial.
	err = clientSend(ctx, nil, laptop, "nas", src, mustSHA(t, src))

	os.Stderr = old
	_ = capW.Close()

	out, rerr := io.ReadAll(capR)
	if rerr != nil {
		t.Fatal(rerr)
	}

	if err == nil || !strings.Contains(err.Error(), "stash") {
		t.Fatalf("send must fail on the stash dial, got %v", err)
	}

	if !strings.Contains(string(out), "dialing nas directly") {
		t.Fatalf("the cached address must be dialed first, stderr:\n%s", out)
	}
}

// TestOpRemove: over the wire, self-removal is anyone's; removing
// another member is admin-only, and the admin's remove sweeps the
// target's parked items.
func TestOpRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := newTestStash(t)
	laptop := testPeerID("laptop") // first join: the bootstrap admin
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("for nas"), 0o600); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, laptop)
	if err := clientSend(ctx, conn, laptop, "nas", src, mustSHA(t, src)); err != nil {
		t.Fatalf("send to nas: %v", err)
	}

	_ = conn.Close()

	// nas is a plain member: removing laptop is not nas's call.
	nasConn := dial(t, st, nas)
	if err := clientRemove(nasConn, "laptop"); err == nil {
		t.Fatal("a non-admin removed another member")
	}

	_ = nasConn.Close()

	if metas, _ := st.spool.items(); len(metas) != 1 || metas[0].Target != "nas" {
		t.Fatalf("non-admin remove touched the spool: %+v", metas)
	}

	if _, ok := memberByName(st.members(), "laptop"); !ok {
		t.Fatalf("non-admin remove dropped laptop: %+v", st.members())
	}

	// The admin removes nas: roster and spool both swept.
	conn = dial(t, st, laptop)
	if err := clientRemove(conn, "nas"); err != nil {
		t.Fatalf("admin remove: %v", err)
	}

	_ = conn.Close()

	if _, ok := memberByName(st.members(), "nas"); ok {
		t.Fatalf("nas still in roster: %+v", st.members())
	}

	if metas, _ := st.spool.items(); len(metas) != 0 {
		t.Fatalf("admin remove left parked items: %+v", metas)
	}
}

// TestRemoveMemberLocal: the stash-side remove rewrites the roster on
// disk and sweeps the member's parked items.
func TestRemoveMemberLocal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	laptop, nas := testPeerID("laptop"), testPeerID("nas")

	// The stash identity: the stash-side commands refuse a dir
	// without one.
	if _, _, err := loadStashIdentity(context.Background(), dir, 1); err != nil {
		t.Fatal(err)
	}

	members := []member{
		{Name: "laptop", Key: laptop.Key.Public(), DialKey: laptop.DialKey.Public(), Joined: 1},
		{Name: "nas", Key: nas.Key.Public(), DialKey: nas.DialKey.Public(), Joined: 2},
	}

	if err := saveRoster(rosterPath(dir), members); err != nil {
		t.Fatal(err)
	}

	sp, err := openSpool(spoolDir(dir), testLogger())
	if err != nil {
		t.Fatal(err)
	}

	var sealed bytes.Buffer
	if _, err := sealStream(laptop.Key, nas.Key.Public(), &sealed, strings.NewReader("x"), shaHexOf([]byte("x")), 0); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"nas", "laptop"} {
		if _, err := sp.put(bytes.NewReader(sealed.Bytes()), spoolMeta{ID: newID(), FileName: "f", SHA: shaHexOf([]byte("x")), From: "laptop", Target: target, Plain: 1, Size: int64(sealed.Len()), At: 1}, 1<<20, 0); err != nil {
			t.Fatal(err)
		}
	}

	if err := removeMemberLocal(context.Background(), dir, "nas", testLogger()); err != nil {
		t.Fatalf("remove: %v", err)
	}

	kept, err := loadRoster(rosterPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	if len(kept) != 1 || kept[0].Name != "laptop" {
		t.Fatalf("roster = %+v", kept)
	}

	metas, err := sp.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 1 || metas[0].Target != "laptop" {
		t.Fatalf("parked items = %+v", metas)
	}

	if err := removeMemberLocal(context.Background(), dir, "ghost", testLogger()); err == nil {
		t.Fatal("removing an unknown member should fail")
	}
}

// TestStashReloadsExternalRosterEdit: a roster edited out of band (the
// stash-side remove) takes effect without restarting serve.
func TestStashReloadsExternalRosterEdit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	if err := saveRoster(rosterPath(st.dir), []member{{Name: "laptop", Key: laptop.Key.Public(), DialKey: laptop.DialKey.Public(), Joined: 1}}); err != nil {
		t.Fatal(err)
	}

	conn := dial(t, st, nas)
	if err := writeMsg(conn, msg{Op: opPending}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	if m.OK || m.Err != "not a member; run catbox join" {
		t.Fatalf("external edit not picked up: %+v", m)
	}
}

// TestOpRemoveSelf: an empty target removes the requester — reset's
// leave. The dial key binds it: only the member itself can reset it.
func TestOpRemoveSelf(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := newTestStash(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	conn := dial(t, st, laptop)
	if err := clientRemove(conn, ""); err != nil {
		t.Fatalf("self remove: %v", err)
	}

	_ = conn.Close()

	for _, m := range st.members() {
		if m.Name == "laptop" {
			t.Fatalf("laptop still in roster: %+v", st.members())
		}
	}

	laptopConn := dial(t, st, laptop)
	if err := writeMsg(laptopConn, msg{Op: opPending}); err != nil {
		t.Fatal(err)
	}

	m, err := readMsg(laptopConn)
	if err != nil {
		t.Fatal(err)
	}

	if m.OK || m.Err != "not a member; run catbox join" {
		t.Fatalf("reset member still served: %+v", m)
	}
}
