package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeTag(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want string
	}{
		{name: "slashes replaced", tag: "AC/DC", want: "AC_DC"},
		{name: "trimmed", tag: "  Artist  ", want: "Artist"},
		{name: "parent reference neutralized", tag: "..", want: "_"},
		{name: "current reference neutralized", tag: ".", want: "_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTag(tt.tag); got != tt.want {
				t.Errorf("sanitizeTag(%q) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

func TestGetOutDirPathStaysUnderOutput(t *testing.T) {
	// Tags are sanitized in buildAlbums before they reach getOutDirPath;
	// a file tagged ARTIST/ALBUM ".." must not resolve outside the output
	// directory once it goes through that flow.
	out := t.TempDir()

	got := getOutDirPath(out, sanitizeTag(".."), sanitizeTag(".."), "2024")

	want := filepath.Join(out, "_", "_ (2024)")
	if got != want {
		t.Errorf("getOutDirPath escaped or changed shape: %q, want %q", got, want)
	}

	if !strings.HasPrefix(got, out) {
		t.Errorf("getOutDirPath(...) = %q, want under %q", got, out)
	}
}
