package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		name      string
		cloneAddr string
		expected  string
		wantErr   bool
	}{
		{"https with .git", "https://github.com/cterence/homelab-gitops.git", "homelab-gitops", false},
		{"https without .git", "https://github.com/cterence/homelab-gitops", "homelab-gitops", false},
		{"trailing slash", "https://github.com/cterence/homelab-gitops.git/", "homelab-gitops", false},
		{"ssh url", "ssh://git@github.com/cterence/homelab-gitops.git", "homelab-gitops", false},
		{"bare scp-like shorthand", "git@github.com:cterence/homelab-gitops.git", "", true},
		{"host only", "https://github.com", "", true},
		{"empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repoNameFromURL(tt.cloneAddr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("repoNameFromURL(%q) error = %v, wantErr %v", tt.cloneAddr, err, tt.wantErr)
			}

			if got != tt.expected {
				t.Errorf("repoNameFromURL(%q) = %q, want %q", tt.cloneAddr, got, tt.expected)
			}
		})
	}
}

func TestRepoKeyIsCaseInsensitive(t *testing.T) {
	if got, want := repoKey("Terence", "Homelab-Gitops"), "terence/homelab-gitops"; got != want {
		t.Errorf("repoKey = %q, want %q", got, want)
	}
}

func TestLoadConfigPrivateTriState(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name      string
		yaml      string
		wantNil   bool
		wantValue bool
	}{
		{name: "unset private stays nil (unmanaged)", yaml: "defaultOwner: terence\nmirrors:\n  - clone_addr: https://github.com/foo/bar.git\n", wantNil: true},
		{name: "private true", yaml: "defaultOwner: terence\nmirrors:\n  - clone_addr: https://github.com/foo/bar.git\n    private: true\n", wantValue: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(dir, "mirrors.yaml")
			if err := os.WriteFile(file, []byte(tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := loadConfig(file)
			if err != nil {
				t.Fatalf("loadConfig unexpected error: %v", err)
			}

			if len(cfg.Mirrors) != 1 {
				t.Fatalf("loaded %d mirrors, want 1", len(cfg.Mirrors))
			}

			got := cfg.Mirrors[0].Private
			if tt.wantNil {
				if got != nil {
					t.Errorf("Private = %v, want nil (config omission must not flip the repo)", *got)
				}

				return
			}

			if got == nil {
				t.Fatal("Private = nil, want a value")
			}

			if *got != tt.wantValue {
				t.Errorf("Private = %v, want %v", *got, tt.wantValue)
			}
		})
	}
}

func TestLoadConfigDuplicateCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "mirrors.yaml")
	yaml := "defaultOwner: terence\nmirrors:\n  - clone_addr: https://github.com/foo/bar.git\n  - clone_addr: https://github.com/Foo/Bar.git\n"

	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadConfig(file); err == nil {
		t.Error("loadConfig expected duplicate error for entries differing only in case")
	}
}

func TestUpdateRepoPrivatePayload(t *testing.T) {
	tests := []struct {
		name       string
		private    *bool
		wantInBody bool
		wantValue  bool
	}{
		{name: "nil private omits the key so Gitea keeps its current setting", private: nil, wantInBody: false},
		{name: "explicit true is sent", private: &[]bool{true}[0], wantInBody: true, wantValue: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]any

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPatch {
					t.Errorf("method = %s, want PATCH", r.Method)
				}

				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Errorf("decode request body: %v", err)
				}

				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c := newClient(srv.URL, "user", "pass")
			m := Mirror{Owner: "terence", Name: "foo", MirrorInterval: "8h", Private: tt.private}

			if err := c.updateRepo(context.Background(), m); err != nil {
				t.Fatalf("updateRepo unexpected error: %v", err)
			}

			_, inBody := gotBody["private"]
			if inBody != tt.wantInBody {
				t.Errorf("payload private present = %v, want %v", inBody, tt.wantInBody)
			}

			if tt.wantInBody && gotBody["private"] != tt.wantValue {
				t.Errorf("payload private = %v, want %v", gotBody["private"], tt.wantValue)
			}
		})
	}
}
