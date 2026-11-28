package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConverter(t *testing.T) *converter {
	t.Helper()

	parsed, err := parseBackup(strings.NewReader(`
{"type":"table","table":"jf_libraries"}
{"type":"row","table":"jf_libraries","data":{"Id":"lib-shows","Name":"Shows"}}
{"type":"row","table":"jf_libraries","data":{"Id":"lib-movies","Name":"Movies"}}
{"type":"row","table":"jf_library_items","data":{"Id":"series-1","Name":"Bref","Type":"Series","ParentId":"lib-shows"}}
{"type":"row","table":"jf_library_items","data":{"Id":"movie-1","Name":"Sinners","Type":"Movie","ParentId":"lib-movies","RunTimeTicks":"81000000000"}}
{"type":"row","table":"jf_library_seasons","data":{"Id":"season-2","Name":"Season 2","Type":"Season","ParentId":"lib-shows"}}
{"type":"row","table":"jf_library_episodes","data":{"Id":"ep-6","Name":"Bref. Is It Serious?","Type":"Episode","SeriesId":"series-1","SeasonId":"season-2","IndexNumber":6,"ParentIndexNumber":2,"RunTimeTicks":"23520000000"}}
{"type":"row","table":"jf_playback_activity","data":{"Id":"play-1","UserId":"user-1","UserName":"terence","Client":"Jellyfin Web","DeviceName":"Firefox","NowPlayingItemId":"movie-1","NowPlayingItemName":"Sinners","PlaybackDuration":"8168","ActivityDateInserted":"2026-08-08T21:22:20.271Z","PlayMethod":"DirectPlay","OriginalContainer":"MKV","MediaStreams":[{"Type":"Video","Codec":"h264"},{"Type":"Audio","Codec":"eac3"}],"PlayState":{"PositionTicks":80000000000}}}
`))
	if err != nil {
		t.Fatalf("parseBackup: %v", err)
	}

	zone, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	return newConverter(parsed, zone)
}

// asMap turns a row into column name -> value, so tests name the fields they
// care about instead of counting offsets.
func asMap(t *testing.T, row []string) map[string]string {
	t.Helper()

	if len(row) != len(historyColumns) {
		t.Fatalf("row has %d columns, want %d", len(row), len(historyColumns))
	}

	fields := make(map[string]string, len(row))
	for i, column := range historyColumns {
		fields[column] = row[i]
	}

	return fields
}

func TestRowMapping(t *testing.T) {
	conv := testConverter(t)

	movie := conv.backup.plays[0]
	episode := movie
	episode.ID = "play-2"
	episode.ItemID = "series-1"
	episode.ItemName = "Bref. Is It Serious?"
	episode.SeriesName = "Bref"
	episode.SeasonID = "season-2"
	episode.Duration = 2398
	episode.Inserted = "2025-03-29T22:51:41.562Z"
	episode.Method = "Transcode"
	episode.Position = 0
	episode.Transcode = &transcode{
		VideoCodec:       "h264",
		AudioCodec:       "aac",
		Container:        "mp4",
		VideoDirect:      boolPtr(true),
		AudioDirect:      boolPtr(false),
		TranscodeReasons: []string{"AudioCodecNotSupported"},
	}

	tests := []struct {
		name  string
		play  play
		want  map[string]string
		model stats
	}{
		{
			name:  "movie direct play keeps its own runtime and position",
			play:  movie,
			model: stats{},
			want: map[string]string{
				"jellydash_history_version": "2",
				"jellydash_timezone":        "Europe/Paris",
				"session_key":               "jellystat-play-1",
				"started_at":                "2026-08-08 23:22:20",
				"ended_at":                  "2026-08-09 01:38:28",
				"item_id":                   "movie-1",
				"item_type":                 "Movie",
				"library":                   "Movies",
				"play_method":               "DirectPlay",
				"play_method_detail":        "Direct Play",
				"source_video_codec":        "h264",
				"source_audio_codec":        "eac3",
				"source_container":          "mkv",
				"is_video_direct":           "1",
				"is_audio_direct":           "1",
				"watched_sec":               "8168",
				"runtime_sec":               "8100",
				"is_finished":               "1",
				"watch_duration_sec":        "8168",
				"started_at_epoch":          "1786224140",
			},
		},
		{
			name:  "series play resolves the episode and its runtime",
			play:  episode,
			model: stats{episodesMatched: 1},
			want: map[string]string{
				"item_id":                   "series-1",
				"item_type":                 "Episode",
				"series_name":               "Bref",
				"season_ep":                 "S02E06",
				"library":                   "Shows",
				"runtime_sec":               "2352",
				"play_method":               "Transcode",
				"play_method_detail":        "Audio Transcode",
				"target_video_codec":        "",
				"target_audio_codec":        "aac",
				"target_container":          "mp4",
				"is_video_direct":           "1",
				"is_audio_direct":           "0",
				"transcode_reasons":         `["AudioCodecNotSupported"]`,
				"is_finished":               "0",
				"started_at":                "2025-03-29 23:51:41",
				"session_key":               "jellystat-play-2",
				"jellydash_history_version": "2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row, counts, err := conv.row(tt.play)
			if err != nil {
				t.Fatalf("row: %v", err)
			}

			fields := asMap(t, row)

			for column, want := range tt.want {
				if fields[column] != want {
					t.Errorf("%s = %q, want %q", column, fields[column], want)
				}
			}

			if counts != tt.model {
				t.Errorf("counts = %+v, want %+v", counts, tt.model)
			}
		})
	}
}

func TestRowMappingMatchesBackupRows(t *testing.T) {
	conv := testConverter(t)

	// Timestamps without a zone are rejected rather than silently shifted.
	broken := conv.backup.plays[0]
	broken.Inserted = "not a date"

	if _, _, err := conv.row(broken); err == nil {
		t.Fatal("row with an unusable start time was accepted")
	}
}

func TestParseBackup(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "table markers and unknown tables are ignored",
			input: "{\"type\":\"table\",\"table\":\"jf_users\"}\n{\"type\":\"row\",\"table\":\"jf_users\",\"data\":{\"Id\":\"u\"}}\n{\"type\":\"row\",\"table\":\"jf_playback_activity\",\"data\":{\"Id\":\"p\"}}\n",
		},
		{
			name:    "empty file is rejected",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid json is reported with its line",
			input:   "{}\n{oops\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseBackup(strings.NewReader(tt.input))

			switch {
			case tt.wantErr && err == nil:
				t.Fatal("expected an error")
			case !tt.wantErr && err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNumberAcceptsStringsAndNumbers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  number
	}{
		{name: "string", input: `"8168"`, want: 8168},
		{name: "number", input: `8168`, want: 8168},
		{name: "empty string", input: `""`, want: 0},
		{name: "null", input: `null`, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parsed number
			if err := json.Unmarshal([]byte(tt.input), &parsed); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if parsed != tt.want {
				t.Fatalf("parsed = %d, want %d", parsed, tt.want)
			}
		})
	}
}

func TestFinished(t *testing.T) {
	tests := []struct {
		name     string
		position number
		runtime  number
		want     int
	}{
		{name: "unknown position", position: 0, runtime: 1000, want: 0},
		{name: "no runtime", position: 900, runtime: 0, want: 0},
		{name: "watched to the end", position: 900, runtime: 1000, want: 1},
		{name: "stopped halfway", position: 500, runtime: 1000, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := finished(tt.position, tt.runtime); got != tt.want {
				t.Fatalf("finished(%d, %d) = %d, want %d", tt.position, tt.runtime, got, tt.want)
			}
		})
	}
}

func TestWriteCSV(t *testing.T) {
	conv := testConverter(t)

	path := filepath.Join(t.TempDir(), "history.csv")

	result, err := writeCSV(path, conv)
	if err != nil {
		t.Fatalf("writeCSV: %v", err)
	}

	if result.plays != len(conv.backup.plays) {
		t.Fatalf("plays = %d, want %d", result.plays, len(conv.backup.plays))
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if !strings.HasPrefix(string(written), "\uFEFFjellydash_history_version,") {
		t.Fatalf("missing byte order mark or header: %q", string(written[:40]))
	}

	lines := strings.Split(strings.TrimSuffix(string(written), "\n"), "\n")
	if len(lines) != len(conv.backup.plays)+1 {
		t.Fatalf("wrote %d lines, want %d", len(lines), len(conv.backup.plays)+1)
	}
}

func boolPtr(value bool) *bool { return &value }
