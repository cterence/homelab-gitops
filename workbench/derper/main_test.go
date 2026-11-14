package main

import (
	"os"
	"path/filepath"
	"testing"

	"tailscale.com/types/key"
)

func TestLoadOrCreateKeyStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "derper.key")

	k1 := loadOrCreateKey(path)
	if k1.IsZero() {
		t.Fatal("generated key is zero")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}

	var round key.NodePrivate
	if err := round.UnmarshalText(b); err != nil {
		t.Fatalf("key file does not parse: %v", err)
	}
	if round.Public() != k1.Public() {
		t.Fatal("key file does not round-trip")
	}

	k2 := loadOrCreateKey(path)
	if k2.Public() != k1.Public() {
		t.Fatal("key not stable across loads")
	}
}
