package main

import "testing"

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
