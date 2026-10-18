package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

// The stash address in identity.json stays raw (re-resolvable); the
// relay-embedded form is a cache keyed by the raw address, and a
// failed dial drops it.
func TestStashAddrCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const raw = tailcat.Addr("tcunused") // unresolvable: no network in tests

	if _, _, err := loadPeerID("emu", raw); err != nil {
		t.Fatalf("join: %v", err)
	}

	dir, err := peerDir()
	if err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}

	var saved peerID
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}

	if saved.StashAddr != raw {
		t.Errorf("identity stash address: got %s, want %s", saved.StashAddr, raw)
	}

	ci := tailcat.ConnInfo{RegionID: 7}
	ci.Region = []*tailcfg.DERPRegion{{
		RegionID: 7,
		Nodes:    []*tailcfg.DERPNode{{HostName: "derp.example", RegionID: 7}},
	}}
	resolved := ci.Addr()
	cachePath := filepath.Join(dir, "stash-resolved.json")

	// A cache keyed to a different raw address is never served.
	if err := saveJSON(cachePath, stashAddrCache{Raw: "tcother", Resolved: resolved}); err != nil {
		t.Fatal(err)
	}

	if id, _, err := loadPeerID("", ""); err != nil {
		t.Fatal(err)
	} else if id.StashAddr != raw {
		t.Errorf("dial address: got %s, want %s", id.StashAddr, raw)
	}

	// A matching cache is served to callers.
	if err := saveJSON(cachePath, stashAddrCache{Raw: raw, Resolved: resolved}); err != nil {
		t.Fatal(err)
	}

	if id, _, err := loadPeerID("", ""); err != nil {
		t.Fatal(err)
	} else if id.StashAddr != resolved {
		t.Errorf("dial address: got %s, want %s", id.StashAddr, resolved)
	}

	// A failed dial drops the cache: the next command re-resolves.
	unresolveStash()

	if id, _, err := loadPeerID("", ""); err != nil {
		t.Fatal(err)
	} else if id.StashAddr != raw {
		t.Errorf("dial address after unresolve: got %s, want %s", id.StashAddr, raw)
	}
}

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
