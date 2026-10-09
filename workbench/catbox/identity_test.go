package main

import (
	"os"
	"path/filepath"
	"testing"
)

// resetPeer must leave the config dir with no identity: the next
// loadPeerID reports "no identity" instead of the old member.
func TestResetPeer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, _, err := loadPeerID("emu", "tcunused"); err != nil {
		t.Fatalf("join: %v", err)
	}

	dir, err := peerDir()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("identity not written: %v", err)
	}

	if err := resetPeer(); err != nil {
		t.Fatalf("reset: %v", err)
	}

	if _, _, err := loadPeerID("", ""); err == nil {
		t.Fatal("identity survived the reset")
	}
}
