package main

import (
	"context"
	"testing"
)

// A failed join must roll back the identity it created on disk: the
// app reads "identity exists" as joined and would strand the user on
// an offline main screen, locked out of the join card.
func TestJoinRollbackOnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := runJoin(context.Background(), "emu", "bogus-addr", ""); err == nil {
		t.Fatal("join with a bogus storer address should fail")
	}

	if _, _, err := loadPeerID("", ""); err == nil {
		t.Fatal("failed join left a half-created identity behind")
	}
}
