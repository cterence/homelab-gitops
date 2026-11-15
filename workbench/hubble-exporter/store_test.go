package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairKeyMatchesLegacyFormat(t *testing.T) {
	tests := []struct {
		name string
		pair Pair
		want string
	}{
		{
			"pod to pod",
			Pair{SrcNS: "argocd", SrcPod: "argocd-1", DstKind: "pod", DstNS: "niks3", DstPod: "niks3-1", DstIP: "10.244.0.7", Proto: "TCP", DstPort: 5432, Verdict: "FORWARDED"},
			"argocd\x00argocd-1\x00pod\x00niks3\x00niks3-1\x0010.244.0.7\x00TCP\x005432\x00FORWARDED",
		},
		{
			"world with empty dst identity",
			Pair{SrcNS: "gitea", SrcPod: "gitea-1", DstKind: "world", DstIP: "172.70.183.172", Proto: "TCP", DstPort: 443, Verdict: "FORWARDED"},
			"gitea\x00gitea-1\x00world\x00\x00\x00172.70.183.172\x00TCP\x00443\x00FORWARDED",
		},
		{
			"dropped ipv6 with normalized port",
			Pair{SrcNS: "vaultwarden", SrcPod: "vw-1", DstKind: "world", DstIP: "2606:4700::6810:84e5", Proto: "TCP", DstPort: 0, Verdict: "DROPPED"},
			"vaultwarden\x00vw-1\x00world\x00\x00\x002606:4700::6810:84e5\x00TCP\x000\x00DROPPED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pairKey(tt.pair); got != tt.want {
				t.Errorf("pairKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDNSKey(t *testing.T) {
	if got, want := dnsKey("kube-system", "example.com."), "dns\x00kube-system\x00example.com."; got != want {
		t.Errorf("dnsKey = %q, want %q", got, want)
	}
}

func TestKeysDifferOnTupleFields(t *testing.T) {
	base := Pair{SrcNS: "a", SrcPod: "p1", DstKind: "world", DstIP: "1.1.1.1", Proto: "TCP", DstPort: 443, Verdict: "FORWARDED"}

	other := base

	other.DstPort = 8443
	if pairKey(base) == pairKey(other) {
		t.Error("different ports must produce different keys")
	}

	other = base

	other.Verdict = "DROPPED"
	if pairKey(base) == pairKey(other) {
		t.Error("different verdicts must produce different keys")
	}
}

func TestReadLegacy(t *testing.T) {
	at := time.Date(2026, 11, 6, 23, 22, 37, 0, time.UTC)

	tests := []struct {
		name    string
		content string
		keys    []string
		wantErr bool
	}{
		{
			"pair and dns keys",
			`{"gitea\u0000gitea-1\u0000world\u0000\u0000\u00001.2.3.4\u0000TCP\u0000443\u0000FORWARDED": "2026-11-06T23:22:37Z", "dns\u0000kube-system\u0000example.com.": "2026-11-06T23:22:37Z"}`,
			[]string{"gitea\x00gitea-1\x00world\x00\x00\x001.2.3.4\x00TCP\x00443\x00FORWARDED", "dns\x00kube-system\x00example.com."},
			false,
		},
		{"not json", "just text", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "seen.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			keys, err := readLegacy(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("readLegacy error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			if len(keys) != len(tt.keys) {
				t.Fatalf("expected %d keys, got %d", len(tt.keys), len(keys))
			}

			for _, k := range tt.keys {
				if !keys[k].Equal(at) {
					t.Errorf("key %q: expected %v, got %v", k, at, keys[k])
				}
			}
		})
	}
}

func TestReadLegacyMissingFile(t *testing.T) {
	keys, err := readLegacy(filepath.Join(t.TempDir(), "seen.json"))
	if err != nil {
		t.Fatalf("readLegacy: %v", err)
	}

	if keys != nil {
		t.Error("missing legacy state must yield a nil map")
	}
}

func TestPairKeyRoundTripsLegacyJSON(t *testing.T) {
	// the migrated keys must be byte-identical to freshly derived ones
	key := pairKey(Pair{SrcNS: "a", SrcPod: "p", DstKind: "pod", DstNS: "b", DstPod: "q", DstIP: "10.0.0.1", Proto: "UDP", DstPort: 53, Verdict: "FORWARDED"})

	data, err := json.Marshal(map[string]time.Time{key: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	keys, err := parseLegacy(data)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := keys[key]; !ok {
		t.Error("derived pair key must survive a legacy json round trip")
	}
}
