# rangemusique

Arranges music files into a clean directory structure based on their
metadata tags. Files are grouped per album and moved (or copied) from an
input directory into `output/artist/album (year)/artist - album - NN track.ext`.

Migrated from https://github.com/cterence/rangemusique.

## How it works

1. **Scan** — walks the input directory and classifies files as audio or
   images (cover art) by content.
2. **Group** — reads tags with taglib (pure Go, CGO-free) and groups tracks
   into albums by artist, album title, and year.
3. **Name** — output paths follow `artist/album (year)/`; track files are
   renamed to `artist - album - number title.ext`. Missing years are looked
   up on MusicBrainz when artist and album are known.
4. **Move** — files are moved by default (`--copy` to copy instead),
   including cross-device moves via copy-and-delete fallback.
5. **Refresh** — optionally triggers a Lidarr folder rescan and a Jellyfin
   library refresh afterwards.

## Lock file

When `--lock-file` points to an existing file, the run is skipped. The
in-cluster deployment uses this to avoid racing with soularr while both
process the same download directory.

## Configuration

All flags can be set via environment variables or a YAML config file
(`--config`, default `config.yaml`):

```yaml
inputDir: /in
outputDir: /out
copy: false
lockFile: /out/.soularr.lock
jellyfin:
  url: http://jellyfin:8096
  apiKey: ...
lidarr:
  url: http://lidarr:8686
  apiKey: ...
discogs:
  token: ...
```

Environment variables map to flags: `INPUT_DIR`, `OUTPUT_DIR`, `COPY`,
`LOCK_FILE`, `JELLYFIN_URL`, `JELLYFIN_API_KEY`, `LIDARR_URL`,
`LIDARR_API_KEY`, `DISCOGS_TOKEN`, `LOG_LEVEL`.

## Development

```bash
go build -o rangemusique .
go test ./...
```

Delete the built binary afterwards.

## Deployment

Consumed by `k8s-apps/arr-stack` (rangemusique CronJob), image
`registry.terence.cloud/rangemusique:<tag>` matching `build.yaml`. Bump both
tags together on code changes.
