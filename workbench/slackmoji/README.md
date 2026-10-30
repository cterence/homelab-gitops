# slackmoji

Small web app that adapts a GIF (or still image) to Slack emoji limits:
at most 128 KB and at most 128x128 px. It keeps every frame and drops
quality first; frames are thinned only as a last resort.

## How it works

1. **Input** — accepts animated GIF, PNG, JPEG or WebP up to 32 MB,
   sniffed by content (not extension).
2. **Pass-through** — input already within the limits is returned
   byte-for-byte, whatever its format.
3. **Shape** — two modes:
   - `ratio`: the longest side becomes 128 px, aspect preserved, never
     upscaled.
   - `square`: stretched to exactly 128x128 px.
4. **Budget search** — the degradation order is: colors binary-searched
   from 255 down to a 16-color floor (dithered; below 16 a GIF
   posterizes, so nothing goes lower), then resolution steps down
   128 → 96 → 64 → 48 px (never above the requested `maxSide`), and
   only then frames, thinned to every other frame (then a quarter, and
   so on) with delays merged so the total duration is preserved.
   Median cut picks one shared palette built from the pixels that
   change between frames; frames after the first store only their
   changed pixels. Frame disposal methods are honored when compositing.

## Endpoints

- `GET /` — web UI: choosing or dropping a file converts immediately;
  the shape toggle and the frame/size presets re-convert. The frame
  presets are percentages of the input's frames (the count is sent as
  `maxFrames`); the size presets are `maxSide` in px.
- `POST /convert` — multipart form with `file`, `mode`
  (`ratio` | `square`) and the optional caps `maxFrames` (drop frames
  down to this count before the quality search; 0 or absent = keep
  every frame) and `maxSide` (32-128 px output size limit; absent =
  128). Returns JSON:
  `{"report": {...}, "data": "data:<mime>;base64,..."}` where `report`
  carries `inputSize`, `outputSize`, `width`, `height`, `framesIn`,
  `framesOut`, `colors`, `format`, `passthrough`.
- `GET /health` — 200 OK, for probes.

Errors return JSON `{"error": "..."}` with status 400 (bad mode or
caps), 413 (over 32 MB) or 422 (undecodable/unsupported input).

Input already within the limits is returned byte-for-byte, unless a
cap would change the output.

## Configuration

- `--port` / `PORT` — listen port (default `3000`).

## Notes

- The UI follows the system colour scheme (light and dark).
- The UI style was dealt with tasteskill (app screen, achromatic-ledger
  style, seed 2121207780).
