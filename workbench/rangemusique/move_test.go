package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(src, []byte("data"), 0666); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile unexpected error: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read moved file: %v", err)
	}

	if string(got) != "data" {
		t.Errorf("moved file content = %q, want %q", got, "data")
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source file still exists after move")
	}
}

func TestMoveFileError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")

	if err := os.WriteFile(src, []byte("data"), 0666); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	// Destination directory does not exist: rename must fail and keep the source.
	if err := moveFile(src, filepath.Join(dir, "nonexistent", "dst.txt")); err == nil {
		t.Fatal("moveFile expected error, got nil")
	}

	if _, err := os.Stat(src); err != nil {
		t.Errorf("source file missing after failed move: %v", err)
	}
}

func TestMoveCrossDevice(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(src, []byte("data"), 0666); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	if err := moveCrossDevice(src, dst); err != nil {
		t.Fatalf("moveCrossDevice unexpected error: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read moved file: %v", err)
	}

	if string(got) != "data" {
		t.Errorf("moved file content = %q, want %q", got, "data")
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source file still exists after cross-device move")
	}
}
