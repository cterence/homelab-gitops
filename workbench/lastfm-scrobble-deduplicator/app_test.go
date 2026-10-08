package main

import (
	"context"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

func TestBelowThreshold(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		prev time.Time
		cur  time.Time
		pct  int
		want bool
	}{
		{name: "below threshold", prev: base, cur: base.Add(30 * time.Second), pct: 50, want: true},
		{name: "above threshold", prev: base, cur: base.Add(3 * time.Minute), pct: 50, want: false},
		{name: "equal timestamps count as not played", prev: base, cur: base, pct: 50, want: true},
		{name: "out-of-order timestamps are never below", prev: base, cur: base.Add(-30 * time.Second), pct: 50, want: false},
		{name: "boundary exactly at threshold", prev: base, cur: base.Add(2 * time.Minute), pct: 50, want: false},
		{name: "capped at 100 percent", prev: base, cur: base.Add(10 * time.Minute), pct: 100, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := &scrobble{artist: "A", track: "T", timestamp: tt.prev, trackDuration: 4 * time.Minute}
			cur := &scrobble{artist: "A", track: "T", timestamp: tt.cur, trackDuration: 4 * time.Minute}

			if got := belowThreshold(prev, cur, tt.pct); got != tt.want {
				t.Errorf("belowThreshold(prev=%v, cur=%v, %d) = %v, want %v", tt.prev, tt.cur, tt.pct, got, tt.want)
			}
		})
	}
}

func TestProcessPreviousAndCurrentScrobbles(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	durations := durationByTrackByArtist{"Artist": {"Track": "4m", "Other": "4m"}}

	tests := []struct {
		name         string
		prev         *scrobble
		cur          *scrobble
		wantReturn   string // "cur" or "prev"
		wantRecorded *scrobble
	}{
		{
			name: "duplicate records the deleted scrobble, not the kept one",
			prev: &scrobble{artist: "Artist", track: "Track", timestamp: base},
			cur:  &scrobble{artist: "Artist", track: "Track", timestamp: base.Add(30 * time.Second)},
			// The previous (older) scrobble is deleted, so it is the one recorded;
			// the current scrobble is kept for the next comparison.
			wantRecorded: &scrobble{artist: "Artist", track: "Track", timestamp: base},
			wantReturn:   "cur",
		},
		{
			name: "incomplete scrobble records the current scrobble",
			prev: &scrobble{artist: "Artist", track: "Track", timestamp: base},
			cur:  &scrobble{artist: "Artist", track: "Other", timestamp: base.Add(30 * time.Second)},
			// The current scrobble is deleted and recorded; the previous is kept for the next comparison.
			wantRecorded: &scrobble{artist: "Artist", track: "Other", timestamp: base.Add(30 * time.Second)},
			wantReturn:   "prev",
		},
		{
			name: "long gap between different tracks records nothing",
			prev: &scrobble{artist: "Artist", track: "Track", timestamp: base},
			cur:  &scrobble{artist: "Artist", track: "Other", timestamp: base.Add(3 * time.Minute)},
			// 75% of the duration: not incomplete at threshold 50.
			wantRecorded: nil,
			wantReturn:   "cur",
		},
		{
			name:         "out-of-order timestamps record nothing",
			prev:         &scrobble{artist: "Artist", track: "Track", timestamp: base},
			cur:          &scrobble{artist: "Artist", track: "Track", timestamp: base.Add(-30 * time.Second)},
			wantRecorded: nil,
			wantReturn:   "cur",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				CanDelete:             false, // dry run: decisions recorded, no deletion attempted
				DuplicateThreshold:    90,
				CompleteThreshold:     50,
				unknownTrackDurations: durationByTrackByArtist{},
			}

			wantCount := 0
			if tt.wantRecorded != nil {
				wantCount = 1
			}

			got := processPreviousAndCurrentScrobbles(context.Background(), c, tt.prev, tt.cur, durations)

			if len(c.deletedScrobbles) != wantCount {
				t.Fatalf("deletedScrobbles count = %d, want %d", len(c.deletedScrobbles), wantCount)
			}

			if tt.wantRecorded != nil {
				recorded := c.deletedScrobbles[0]
				if recorded.artist != tt.wantRecorded.artist || recorded.track != tt.wantRecorded.track {
					t.Errorf("recorded scrobble = %s - %s, want %s - %s", recorded.artist, recorded.track, tt.wantRecorded.artist, tt.wantRecorded.track)
				}

				if !recorded.timestamp.Equal(tt.wantRecorded.timestamp) {
					t.Errorf("recorded timestamp = %v, want %v (the scrobble that gets deleted)", recorded.timestamp, tt.wantRecorded.timestamp)
				}
			}

			want := tt.cur
			if tt.wantReturn == "prev" {
				want = tt.prev
			}

			if got != want {
				t.Errorf("returned scrobble = %+v, want the %s scrobble", got, tt.wantReturn)
			}
		})
	}
}

func TestWriteUnknownTrackDurationsTruncatesStaleTail(t *testing.T) {
	dir := t.TempDir()

	stale := strings.Repeat("# stale line that must not survive a shorter rewrite\n", 50)
	if err := os.WriteFile(path.Join(dir, customTrackDurationsFile), []byte(stale), 0o666); err != nil {
		t.Fatal(err)
	}

	if err := writeUnknownTrackDurations(durationByTrackByArtist{"Artist": {"Track": ""}}, dir); err != nil {
		t.Fatalf("writeUnknownTrackDurations unexpected error: %v", err)
	}

	content, err := os.ReadFile(path.Join(dir, customTrackDurationsFile))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(content), "stale line") {
		t.Error("durations file still contains stale content from a previous longer write")
	}

	parsed, err := getUserTrackDurations(dir)
	if err != nil {
		t.Fatalf("rewritten file no longer parses: %v", err)
	}

	if _, ok := parsed["Artist"]["Track"]; !ok {
		t.Errorf("rewritten file missing expected entry, got %v", parsed)
	}
}

func TestXPathString(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain", value: `Foo`, want: `'Foo'`},
		{name: "apostrophe uses double quotes", value: `Don't Stop`, want: `"Don't Stop"`},
		{name: "double quote uses single quotes", value: `Say "Hi"`, want: `'Say "Hi"'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xpathString(tt.value); got != tt.want {
				t.Errorf("xpathString(%q) = %s, want %s", tt.value, got, tt.want)
			}
		})
	}

	t.Run("both quote kinds use concat", func(t *testing.T) {
		got := xpathString(`Don't say "Hi"`)
		if !strings.HasPrefix(got, "concat(") || !strings.Contains(got, `"'"`) {
			t.Errorf("xpathString with both quote kinds = %s, want concat() joining the parts", got)
		}
	})
}

func TestScrobbleXPath(t *testing.T) {
	t.Run("anchors on artist and track", func(t *testing.T) {
		got := scrobbleXPath("Artist", "Track", "1754948517", false)

		want := `(//tr[contains(@class,'chartlist-row')][.//input[@name='artist_name' and @value='Artist']][.//input[@name='track_name' and @value='Track']]//input[@name='timestamp' and @value='1754948517'])`
		if got != want {
			t.Errorf("scrobbleXPath = %s, want %s", got, want)
		}
	})

	t.Run("delete current selects the last match", func(t *testing.T) {
		got := scrobbleXPath("A", "T", "123", true)
		if !strings.HasSuffix(got, `[last()]`) {
			t.Errorf("scrobbleXPath with last = %s, want [last()] suffix", got)
		}
	})

	t.Run("apostrophes are quoted safely", func(t *testing.T) {
		got := scrobbleXPath("AC/DC's", "T", "123", false)
		if !strings.Contains(got, `@value="AC/DC's"`) {
			t.Errorf("scrobbleXPath with apostrophe = %s, want double-quoted artist value", got)
		}
	})
}
