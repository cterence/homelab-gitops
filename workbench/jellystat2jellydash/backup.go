package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// backup holds the rows of a Jellystat JSONL backup, keyed by table.
type backup struct {
	plays     []play
	items     map[string]item
	episodes  map[string]item
	seasons   map[string]item
	libraries map[string]item
}

// entry is one JSONL line: a table marker or a row of that table.
type entry struct {
	Type  string          `json:"type"`
	Table string          `json:"table"`
	Data  json.RawMessage `json:"data"`
}

// play is one jf_playback_activity row: a finished viewing with its total.
type play struct {
	ID         string          `json:"Id"`
	UserID     string          `json:"UserId"`
	UserName   string          `json:"UserName"`
	Client     string          `json:"Client"`
	Device     string          `json:"DeviceName"`
	ItemID     string          `json:"NowPlayingItemId"`
	ItemName   string          `json:"NowPlayingItemName"`
	SeriesName string          `json:"SeriesName"`
	SeasonID   string          `json:"SeasonId"`
	Duration   number          `json:"PlaybackDuration"`
	Inserted   string          `json:"ActivityDateInserted"`
	Method     string          `json:"PlayMethod"`
	Container  string          `json:"OriginalContainer"`
	Streams    []stream        `json:"MediaStreams"`
	Transcode  *transcode      `json:"TranscodingInfo"`
	Position   number          `json:"-"`
	PlayState  json.RawMessage `json:"PlayState"`
}

type stream struct {
	Type  string `json:"Type"`
	Codec string `json:"Codec"`
}

type transcode struct {
	VideoCodec       string   `json:"VideoCodec"`
	AudioCodec       string   `json:"AudioCodec"`
	Container        string   `json:"Container"`
	VideoDirect      *bool    `json:"IsVideoDirect"`
	AudioDirect      *bool    `json:"IsAudioDirect"`
	TranscodeReasons []string `json:"TranscodeReasons"`
}

// item covers jf_library_items, jf_library_episodes, jf_library_seasons and
// jf_libraries, which share the fields the conversion needs.
type item struct {
	ID                string `json:"Id"`
	Name              string `json:"Name"`
	Type              string `json:"Type"`
	ParentID          string `json:"ParentId"`
	SeriesID          string `json:"SeriesId"`
	SeasonID          string `json:"SeasonId"`
	Runtime           number `json:"RunTimeTicks"`
	IndexNumber       *int   `json:"IndexNumber"`
	ParentIndexNumber *int   `json:"ParentIndexNumber"`
}

// number decodes a JSON number or a number quoted as a string: Jellystat
// exports ticks and durations in whichever form the column had.
type number int64

func (n *number) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if raw == "" || raw == "null" {
		*n = 0

		return nil
	}

	var parsed float64
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return fmt.Errorf("reading number %q: %w", raw, err)
	}

	*n = number(parsed)

	return nil
}

func readBackup(path string) (*backup, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, notFoundError(fmt.Errorf("opening %s: %w", path, err))
	}
	defer func() { _ = file.Close() }()

	return parseBackup(file)
}

func parseBackup(r io.Reader) (*backup, error) {
	parsed := &backup{
		items:     map[string]item{},
		episodes:  map[string]item{},
		seasons:   map[string]item{},
		libraries: map[string]item{},
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for line := 1; scanner.Scan(); line++ {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}

		var row entry
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("decoding line %d: %w", line, err)
		}

		if row.Type != "row" {
			continue
		}

		var target map[string]item

		switch row.Table {
		case "jf_playback_activity":
			var recorded play
			if err := json.Unmarshal(row.Data, &recorded); err != nil {
				return nil, fmt.Errorf("decoding play on line %d: %w", line, err)
			}

			recorded.Position = positionTicks(recorded.PlayState)
			parsed.plays = append(parsed.plays, recorded)

			continue
		case "jf_library_items":
			target = parsed.items
		case "jf_library_episodes":
			target = parsed.episodes
		case "jf_library_seasons":
			target = parsed.seasons
		case "jf_libraries":
			target = parsed.libraries
		default:
			continue
		}

		var decoded item
		if err := json.Unmarshal(row.Data, &decoded); err != nil {
			return nil, fmt.Errorf("decoding %s on line %d: %w", row.Table, line, err)
		}

		target[decoded.ID] = decoded
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading backup: %w", err)
	}

	if len(parsed.plays) == 0 {
		return nil, fmt.Errorf("%w: no jf_playback_activity rows, is this a Jellystat backup?", errNoPlays)
	}

	return parsed, nil
}

// positionTicks reads the last known position out of PlayState, which Jellystat
// stores as either a JSON object or a stringified one.
func positionTicks(raw json.RawMessage) number {
	candidate := strings.TrimSpace(string(raw))
	if candidate == "" || candidate == "null" {
		return 0
	}

	if strings.HasPrefix(candidate, `"`) {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return 0
		}

		candidate = inner
	}

	var state struct {
		PositionTicks number `json:"PositionTicks"`
	}

	if err := json.Unmarshal([]byte(candidate), &state); err != nil {
		return 0
	}

	return state.PositionTicks
}

var errNoPlays = errors.New("no plays found")
