package main

import (
	"strings"
	"testing"
	"time"
)

// TestJoinAdmission pins the whole admission flow: the first member
// bootstraps as admin without a code, later joins need a live
// one-time code an admin minted, and a burned or expired code is
// refused.
func TestJoinAdmission(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := newTestStorer(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")
	phone := testPeerID("phone")

	// Bootstrap: the first member ever joins free and is admin.
	conn := dial(t, st, laptop)
	if err := joinReq(conn, laptop, "", ""); err != nil {
		t.Fatalf("bootstrap join: %v", err)
	}

	_ = conn.Close()

	m, ok := memberByName(st.members(), "laptop")
	if !ok || !m.Admin {
		t.Fatalf("first member must bootstrap as admin: %+v", st.members())
	}

	// No code: refused.
	conn = dial(t, st, nas)
	if err := joinReq(conn, nas, "", ""); err == nil || !strings.Contains(err.Error(), "invite code") {
		t.Fatalf("codeless join must be refused, got %v", err)
	}

	_ = conn.Close()

	// A wrong or expired code is refused too.
	st.mu.Lock()
	st.invites = append(st.invites, invite{Code: "expired", Expires: time.Now().Add(-time.Minute).Unix()})
	st.mu.Unlock()

	conn = dial(t, st, nas)
	if err := joinReq(conn, nas, "", "expired"); err == nil {
		t.Fatal("an expired code must be refused")
	}

	_ = conn.Close()

	conn = dial(t, st, nas)
	if err := joinReq(conn, nas, "", "wrong"); err == nil {
		t.Fatal("an unknown code must be refused")
	}

	_ = conn.Close()

	// The admin mints; the join rides the code.
	conn = dial(t, st, laptop)

	code, err := clientInvite(conn)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	_ = conn.Close()

	if code == "" {
		t.Fatal("invite minted an empty code")
	}

	conn = dial(t, st, nas)
	if err := joinReq(conn, nas, "", code); err != nil {
		t.Fatalf("join with code: %v", err)
	}

	_ = conn.Close()

	// The code is single-use.
	conn = dial(t, st, phone)
	if err := joinReq(conn, phone, "", code); err == nil {
		t.Fatal("a burned code must be refused")
	}

	_ = conn.Close()
}

// TestInviteRequiresAdmin: minting is the admin's call.
func TestInviteRequiresAdmin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := newTestStorer(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	conn := dial(t, st, nas)
	defer func() { _ = conn.Close() }()

	if _, err := clientInvite(conn); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("non-admin invite must be refused, got %v", err)
	}
}

// TestInviteStoreRoundtrip: expired codes are pruned on load.
func TestInviteStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()

	if err := saveInvites(invitePath(dir), []invite{
		{Code: "live", Expires: now + 60},
		{Code: "dead", Expires: now - 60},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := loadInvites(invitePath(dir))
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Code != "live" {
		t.Fatalf("invites = %+v, want only live", got)
	}
}

// TestGrantAdmin: the storer-side grant promotes a member on disk,
// and the running storer picks it up on its next message.
func TestGrantAdmin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := newTestStorer(t)
	laptop := testPeerID("laptop")
	nas := testPeerID("nas")

	for _, id := range []*peerID{laptop, nas} {
		conn := dial(t, st, id)
		if err := joinReq(conn, id, "", testInvite(t, st)); err != nil {
			t.Fatalf("join %s: %v", id.Name, err)
		}

		_ = conn.Close()
	}

	if err := grantAdmin(st.dir, "nas"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	if err := grantAdmin(st.dir, "ghost"); err == nil {
		t.Fatal("granting an unknown member must fail")
	}

	// reloadRoster picks the edit up; nas can now mint.
	conn := dial(t, st, nas)
	defer func() { _ = conn.Close() }()

	if _, err := clientInvite(conn); err != nil {
		t.Fatalf("granted admin cannot invite: %v", err)
	}
}
