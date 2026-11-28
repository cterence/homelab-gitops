# jellystat2jellydash

Converts a Jellystat backup into the History CSV that Jellydash imports, so
viewing history survives a move from Jellystat to Jellydash.

## Why

Jellydash has no Jellystat importer. It does import its own History CSV
(`Settings → Import play history`, format version 2), so the backup Jellystat
writes is translated into that format and uploaded through the UI. The
conversion is one-way and safe to repeat: Jellydash skips plays whose
`session_key` and `item_id` already exist.

## Usage

```console
jellystat2jellydash <backup.jsonl> <output.csv> [--timezone <IANA name>] [--json]
```

- `backup.jsonl` — the file Jellystat exports from its database
  (`backup_<date>.jsonl`).
- `output.csv` — the History CSV to upload in Jellydash.
- `--timezone` — the IANA zone Jellydash runs in. Every timestamp is written in
  it, with the matching Unix epoch, so a daylight-saving change cannot shift a
  play. Defaults to `$TZ`, then the local zone.
- `--json` — structured log records on stderr instead of text.

Run it with `go run . <backup.jsonl> <output.csv>`, or build a binary. It reads
the backup into memory (a full history is tens of megabytes) and writes the CSV
through a temporary file, so a failure never leaves a partial import file.

## Mapping

| Jellydash column | Jellystat source |
| --- | --- |
| `started_at`, `started_at_epoch` | `jf_playback_activity.ActivityDateInserted` (UTC, converted to `--timezone`) |
| `ended_at`, `updated_at` | start + `PlaybackDuration` |
| `watched_sec`, `watch_duration_sec` | `PlaybackDuration` (seconds) |
| `item_id` | `NowPlayingItemId` |
| `item_name` | `NowPlayingItemName` |
| `item_type` | `jf_library_items.Type`; a play with a `SeriesName` becomes `Episode` |
| `series_name` | `SeriesName` |
| `season_ep` | episode matched by series and title, else the season name |
| `library` | parent of the item, resolved through `jf_libraries` |
| `runtime_sec` | `RunTimeTicks / 1e7` of the episode, else of the item |
| `is_finished` | `PlayState.PositionTicks` at or above 90% of the runtime |
| `play_method`, `play_method_detail` | `PlayMethod`, refined with `TranscodingInfo.IsVideoDirect` / `IsAudioDirect` |
| codecs, containers, `transcode_reasons` | `MediaStreams`, `OriginalContainer`, `TranscodingInfo` |
| `session_key` | `jellystat-<row Id>`, so imported plays never collide with live ones |

## What the backup cannot tell you

- **One total per viewing, not a curve.** Jellystat stores the session duration,
  so the imported row is a reported duration — the same class as Jellydash's
  Playback Reporting import. Pauses inside that window are not visible.
- **TV plays reference the series.** Jellystat stores the series id as the
  played item and the episode title separately. The episode is matched by series
  plus title: unmatched plays keep the series id and the season name only.
- **Durations above the media runtime.** Jellystat counts session time, so some
  rows exceed the item's `RunTimeTicks`; the value is left as reported.
- **Short plays are gone.** Anything below Jellystat's
  `MINIMUM_SECONDS_TO_INCLUDE_PLAYBACK` was never written to the backup.
- **Music dominates most backups** and imports as `item_type = Audio`. Jellydash
  shows those plays in History, but its series and movie statistics key on
  `Episode` and `Movie`, so music stays out of them.
- **Deleted media.** Plays whose item is no longer in `jf_library_items` are
  imported as `Video` with no library, keeping their title and duration.
