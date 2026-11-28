package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// historyColumns is the Jellydash History CSV v2 header, in order. Jellydash
// rejects any other header, so this list is the contract.
var historyColumns = []string{
	"jellydash_history_version",
	"jellydash_timezone",
	"session_key",
	"started_at",
	"updated_at",
	"ended_at",
	"user_id",
	"user_name",
	"item_id",
	"item_type",
	"series_name",
	"item_name",
	"season_ep",
	"library",
	"library_resolved_at",
	"play_method",
	"play_method_detail",
	"client",
	"device",
	"source_video_codec",
	"source_audio_codec",
	"source_container",
	"target_video_codec",
	"target_audio_codec",
	"target_container",
	"is_video_direct",
	"is_audio_direct",
	"transcode_reasons",
	"watched_sec",
	"runtime_sec",
	"is_finished",
	"watch_duration_sec",
	"started_at_epoch",
	"updated_at_epoch",
	"ended_at_epoch",
	"library_resolved_at_epoch",
}

const (
	ticksPerSecond = 10_000_000
	// formatDateTime is the only timestamp shape the Jellydash importer parses.
	formatDateTime = "2006-01-02 15:04:05"
	formatVersion  = "2"
)

type stats struct {
	plays           int
	episodesMatched int
	seriesOnly      int
	missingItems    int
}

func convert(ctx context.Context, cfg config) error {
	slog.SetDefault(newLogger(os.Stderr, cfg.json))

	zone, err := resolveZone(cfg.timezone)
	if err != nil {
		return err
	}

	parsed, err := readBackup(cfg.backup)
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	result, err := writeCSV(cfg.output, newConverter(parsed, zone))
	if err != nil {
		return err
	}

	slog.Info("converted",
		"plays", result.plays,
		"episodes_matched", result.episodesMatched,
		"series_without_episode", result.seriesOnly,
		"items_missing", result.missingItems,
		"timezone", zone.String(),
		"output", cfg.output,
	)

	return nil
}

// writeCSV streams the rows through a temporary file so a failure never leaves
// a half-written CSV behind.
func writeCSV(path string, conv *converter) (stats, error) {
	temp := path + ".tmp"

	file, err := os.Create(temp)
	if err != nil {
		return stats{}, fmt.Errorf("creating %s: %w", temp, err)
	}

	result, err := writeRows(file, conv)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("closing %s: %w", temp, closeErr)
	}

	if err != nil {
		_ = os.Remove(temp)

		return stats{}, err
	}

	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)

		return stats{}, fmt.Errorf("writing %s: %w", path, err)
	}

	return result, nil
}

func writeRows(file *os.File, conv *converter) (stats, error) {
	buffered := bufio.NewWriter(file)

	// Jellydash accepts the byte order mark its own exporter writes.
	if _, err := buffered.WriteString("\uFEFF"); err != nil {
		return stats{}, fmt.Errorf("writing header: %w", err)
	}

	writer := csv.NewWriter(buffered)

	if err := writer.Write(historyColumns); err != nil {
		return stats{}, fmt.Errorf("writing header: %w", err)
	}

	result := stats{}

	for _, recorded := range conv.backup.plays {
		row, counts, err := conv.row(recorded)
		if err != nil {
			return stats{}, err
		}

		result.plays++
		result.episodesMatched += counts.episodesMatched
		result.seriesOnly += counts.seriesOnly
		result.missingItems += counts.missingItems

		if err := writer.Write(row); err != nil {
			return stats{}, fmt.Errorf("writing play %s: %w", recorded.ID, err)
		}
	}

	writer.Flush()

	if err := writer.Error(); err != nil {
		return stats{}, fmt.Errorf("writing rows: %w", err)
	}

	if err := buffered.Flush(); err != nil {
		return stats{}, fmt.Errorf("flushing %s: %w", file.Name(), err)
	}

	return result, nil
}

type converter struct {
	backup *backup
	zone   *time.Location
	// byEpisodeName finds the episode a series-level play refers to.
	byEpisodeName map[string][]item
}

func newConverter(parsed *backup, zone *time.Location) *converter {
	byName := map[string][]item{}

	for _, episode := range parsed.episodes {
		key := episode.SeriesID + "\x00" + normalize(episode.Name)
		byName[key] = append(byName[key], episode)
	}

	return &converter{backup: parsed, zone: zone, byEpisodeName: byName}
}

// row maps one Jellystat play onto the columns Jellydash imports.
func (c *converter) row(recorded play) ([]string, stats, error) {
	counts := stats{}

	started, err := time.Parse(time.RFC3339Nano, recorded.Inserted)
	if err != nil {
		return nil, counts, fmt.Errorf("play %s has no usable start time: %w", recorded.ID, err)
	}

	item, found := c.backup.items[recorded.ItemID]
	if !found {
		counts.missingItems++
	}

	episode, season := c.resolveEpisode(recorded, &counts)

	itemType := strings.TrimSpace(item.Type)
	if recorded.SeriesName != "" || episode != nil {
		itemType = "Episode"
	}

	if itemType == "" {
		itemType = "Video"
	}

	runtimeTicks := runtimeOf(episode, &item)
	ended := started.Add(time.Duration(recorded.Duration) * time.Second)
	method, detail := playMethod(recorded)
	videoDirect, audioDirect := directFlags(recorded)
	targetVideo, targetAudio := targetCodecs(recorded)

	return []string{
		formatVersion,
		c.zone.String(),
		"jellystat-" + recorded.ID,
		formatTime(started, c.zone),
		formatTime(ended, c.zone),
		formatTime(ended, c.zone),
		recorded.UserID,
		recorded.UserName,
		recorded.ItemID,
		truncate(itemType, 16),
		recorded.SeriesName,
		recorded.ItemName,
		seasonEpisode(episode, season),
		c.libraryName(item, season),
		"",
		method,
		detail,
		recorded.Client,
		recorded.Device,
		streamCodec(recorded.Streams, "video"),
		streamCodec(recorded.Streams, "audio"),
		strings.ToLower(recorded.Container),
		targetVideo,
		targetAudio,
		transcodeContainer(recorded),
		boolean(videoDirect),
		boolean(audioDirect),
		transcodeReasons(recorded),
		fmt.Sprint(recorded.Duration),
		fmt.Sprint(seconds(runtimeTicks)),
		strconv.Itoa(finished(recorded.Position, runtimeTicks)),
		fmt.Sprint(recorded.Duration),
		strconv.FormatInt(started.Unix(), 10),
		strconv.FormatInt(ended.Unix(), 10),
		strconv.FormatInt(ended.Unix(), 10),
		"",
	}, counts, nil
}

// resolveEpisode finds the episode behind a series-level play. Jellystat stores
// the series id as the played item and the episode title separately, so the
// episode is matched by series and title.
func (c *converter) resolveEpisode(recorded play, counts *stats) (*item, *item) {
	if recorded.SeriesName == "" {
		return nil, nil
	}

	candidates := c.byEpisodeName[recorded.ItemID+"\x00"+normalize(recorded.ItemName)]

	if len(candidates) == 1 {
		counts.episodesMatched++

		episode := candidates[0]

		season, ok := c.backup.seasons[episode.SeasonID]
		if !ok {
			return &episode, nil
		}

		return &episode, &season
	}

	counts.seriesOnly++

	season, ok := c.backup.seasons[recorded.SeasonID]
	if !ok {
		return nil, nil
	}

	return nil, &season
}

// libraryName walks one level up to the library that holds the item.
func (c *converter) libraryName(item item, season *item) string {
	for _, parentID := range []string{item.ParentID, parentOf(season)} {
		if library, ok := c.backup.libraries[parentID]; ok && library.Name != "" {
			return library.Name
		}
	}

	return ""
}

func parentOf(season *item) string {
	if season == nil {
		return ""
	}

	return season.ParentID
}

func runtimeOf(episode *item, item *item) number {
	if episode != nil && episode.Runtime > 0 {
		return episode.Runtime
	}

	return item.Runtime
}

func seconds(ticks number) number {
	// Rounds to the nearest second, halfway cases away from zero.
	if ticks < 0 {
		return (ticks - ticksPerSecond/2) / ticksPerSecond
	}

	return (ticks + ticksPerSecond/2) / ticksPerSecond
}

// finished marks a play whose last known position reached 90% of the runtime.
func finished(position, runtimeTicks number) int {
	if position <= 0 || runtimeTicks <= 0 {
		return 0
	}

	if position*10 >= runtimeTicks*9 {
		return 1
	}

	return 0
}

func formatTime(moment time.Time, zone *time.Location) string {
	return moment.In(zone).Format(formatDateTime)
}

func seasonEpisode(episode *item, season *item) string {
	if episode != nil && episode.IndexNumber != nil && episode.ParentIndexNumber != nil {
		return fmt.Sprintf("S%02dE%02d", *episode.ParentIndexNumber, *episode.IndexNumber)
	}

	if season != nil {
		return strings.TrimSpace(season.Name)
	}

	return ""
}

func streamCodec(streams []stream, kind string) string {
	for _, candidate := range streams {
		if strings.EqualFold(candidate.Type, kind) && candidate.Codec != "" {
			return strings.ToLower(candidate.Codec)
		}
	}

	return ""
}

func playMethod(recorded play) (string, string) {
	switch recorded.Method {
	case "Transcode":
		video, audio := directFlags(recorded)

		switch {
		case isTrue(video) && isTrue(audio):
			return "Transcode", "Remux"
		case isTrue(video):
			return "Transcode", "Audio Transcode"
		default:
			return "Transcode", "Video Transcode"
		}
	case "DirectStream":
		return "DirectStream", "Direct Stream"
	default:
		return "DirectPlay", "Direct Play"
	}
}

// directFlags prefers what the server reported and falls back to the method.
func directFlags(recorded play) (*bool, *bool) {
	if recorded.Transcode != nil && recorded.Transcode.VideoDirect != nil {
		return recorded.Transcode.VideoDirect, recorded.Transcode.AudioDirect
	}

	direct := recorded.Method != "Transcode"

	return &direct, &direct
}

func targetCodecs(recorded play) (string, string) {
	if recorded.Transcode == nil {
		return "", ""
	}

	video, audio := "", ""

	if !isTrue(recorded.Transcode.VideoDirect) {
		video = strings.ToLower(recorded.Transcode.VideoCodec)
	}

	if !isTrue(recorded.Transcode.AudioDirect) {
		audio = strings.ToLower(recorded.Transcode.AudioCodec)
	}

	return video, audio
}

func transcodeContainer(recorded play) string {
	if recorded.Transcode == nil {
		return ""
	}

	return strings.ToLower(recorded.Transcode.Container)
}

func transcodeReasons(recorded play) string {
	if recorded.Transcode == nil || len(recorded.Transcode.TranscodeReasons) == 0 {
		return ""
	}

	encoded, err := json.Marshal(recorded.Transcode.TranscodeReasons)
	if err != nil {
		return ""
	}

	return string(encoded)
}

func boolean(value *bool) string {
	if value == nil {
		return ""
	}

	if *value {
		return "1"
	}

	return "0"
}

func isTrue(value *bool) bool {
	return value != nil && *value
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}

	return string(runes[:limit])
}

// normalize folds a title so small punctuation differences still match.
func normalize(value string) string {
	var folded strings.Builder

	for _, char := range strings.ToLower(value) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			folded.WriteRune(char)
		}
	}

	return folded.String()
}
