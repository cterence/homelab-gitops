package main

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

const evictSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// writePartial creates one deposit partial of n bytes with the given
// mtime, so candidates only differ in size and age.
func writePartial(t *testing.T, s *spool, from string, plain, n int64, mod time.Time) string {
	t.Helper()

	p := spoolPartialPath(s.dir, from, evictSHA, plain)
	if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestEvictPartials(t *testing.T) {
	tests := []struct {
		name    string
		need    int64
		removed []string
		want    int64
	}{
		{
			name:    "the oldest stale partial is enough",
			need:    100,
			removed: []string{"oldest"},
			want:    100,
		},
		{
			name:    "every stale partial is evicted before refusing",
			need:    10000,
			removed: []string{"oldest", "middle", "newest"},
			want:    600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := openSpool(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}

			base := time.Now().Add(-2 * time.Hour)

			// Three stale partials, oldest first: 100, 200, 300 bytes.
			roles := map[string]string{
				"oldest": writePartial(t, s, "alice", 1, 100, base),
				"middle": writePartial(t, s, "alice", 2, 200, base.Add(time.Minute)),
				"newest": writePartial(t, s, "alice", 3, 300, base.Add(2*time.Minute)),
				// An in-flight deposit: claimed, never evicted.
				"active": writePartial(t, s, "bob", 4, 400, base),
				// The incoming deposit's own resume point.
				"keep": writePartial(t, s, "carol", 5, 500, base),
			}

			s.claim("bob", evictSHA, 4, io.NopCloser(nil))

			// A parked body is never eviction material.
			body := spoolBodyPath(s.dir, "dave", evictSHA, 6)
			if err := os.WriteFile(body, make([]byte, 10), 0o600); err != nil {
				t.Fatal(err)
			}

			if got := s.evictPartials(tt.need, roles["keep"]); got != tt.want {
				t.Fatalf("evictPartials(%d) = %d, want %d", tt.need, got, tt.want)
			}

			for _, role := range tt.removed {
				if _, err := os.Stat(roles[role]); !os.IsNotExist(err) {
					t.Fatalf("stale partial survived: %s (%v)", role, err)
				}
			}

			for role, p := range roles {
				if role == "oldest" || role == "middle" || role == "newest" {
					continue
				}

				if _, err := os.Stat(p); err != nil {
					t.Fatalf("active or keep swept: %s (%v)", role, err)
				}
			}

			if _, err := os.Stat(body); err != nil {
				t.Fatalf("parked body swept: %v", err)
			}
		})
	}
}
