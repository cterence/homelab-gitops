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
4. **Budget search** — the encoder binary-searches the largest palette
   that fits: 256 down to 2 colors (median cut, one palette per frame,
   so each frame keeps its own best colors). Input that still exceeds
   128 KB at 2 colors is thinned to every other frame (then a quarter,
   and so on), merging delays so the total duration is preserved.
   128x128 is never shrunk further; the last resort is a single frame
   at 2 colors. Frame disposal methods are honored when compositing,
   and each output frame is full-canvas.

## Endpoints

- `GET /` — web UI: choosing or dropping a file converts immediately;
  the shape toggle and the frame/size sliders re-convert.
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

- `PORT` — listen port (default `3000`).

## Notes

- The UI follows the system colour scheme (light and dark).
- The UI style was dealt with tasteskill (app screen, achromatic-ledger
  style, seed 2121207780).
