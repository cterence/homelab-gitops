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

// resolveDataDir: the explicit flag wins, then CATBOX_DATA, then the
// host default (the config root's stash/ subdir).
func TestResolveDataDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CATBOX_DATA", "")

	conf, err := peerDir()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		flag    string
		env     string
		want    string
		wantErr bool
	}{
		{"flag", "/data", "", "/data", false},
		{"env over default", "", "/srv/catbox", "/srv/catbox", false},
		{"host default", "", "", filepath.Join(conf, "stash"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CATBOX_DATA", tt.env)

			got, err := resolveDataDir(tt.flag)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveDataDir() error = %v", err)
			}

			if got != tt.want {
				t.Fatalf("resolveDataDir() = %q, want %q", got, tt.want)
			}
		})
	}
}
