package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckLockFile(t *testing.T) {
	dir := t.TempDir()

	existing := filepath.Join(dir, ".soularr.lock")
	if err := os.WriteFile(existing, nil, 0666); err != nil {
		t.Fatalf("failed to create lock file: %v", err)
	}

	// A path under a regular file fails stat with a non-NotExist error.
	unstatable := filepath.Join(existing, "nested.lock")

	tests := []struct {
		name    string
		path    string
		want    bool
		wantErr bool
	}{
		{name: "empty path disables locking", path: "", want: false},
		{name: "missing lock file", path: filepath.Join(dir, "missing.lock"), want: false},
		{name: "present lock file", path: existing, want: true},
		{name: "unstatable path", path: unstatable, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := checkLockFile(tt.path)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("checkLockFile(%q) expected error, got %v", tt.path, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("checkLockFile(%q) unexpected error: %v", tt.path, err)
			}

			if got != tt.want {
				t.Errorf("checkLockFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
