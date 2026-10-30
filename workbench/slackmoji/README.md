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
   - `ratio`: the longest side becomes at most 128 px, aspect preserved,
     never upscaled.
   - `square`: stretched to fill the square when converting.
   Input already within the limits is returned byte-for-byte in either
   mode.
4. **Budget search** — degradation is interleaved: colors
   binary-searched from 255 down to a 16-color floor (dithered; below
   16 a GIF posterizes, so nothing goes lower) at 128 px with every
   frame, then at 128 px with half the frames, then 96 px with both
   frame counts, then 64 px, then 48 px. A size step is never traded
   for more than one frame halving: 64 px at half fps beats 48 px at
   full fps. After the sizes are exhausted, frames keep halving at
   48 px. Median cut picks one shared palette built from the pixels
   that change between frames; frames after the first store only
   their changed pixels, and delays merge so total duration is
   preserved. Frame disposal methods are honored when compositing.

## Endpoints

- `GET /` — web UI: choosing or dropping a file converts immediately;
  the shape toggle and the frame/color/size presets re-convert, and
  after every conversion the presets display what the algorithm chose
  (frames as a percentage, size and colors by nearest preset).
- `POST /convert` — multipart form with `file`, `mode`
  (`ratio` | `square`) and the optional caps `maxFrames` (drop frames
  down to this count before the quality search; 0 or absent = keep
  every frame), `maxSide` (32-128 px output size limit; absent = 128)
  and `maxColors` (16-255 palette cap; absent = fill the budget).
  Returns JSON:
  `{"report": {...}, "data": "data:<mime>;base64,..."}` where `report`
  carries `inputSize`, `inputWidth`, `inputHeight`, `outputSize`,
  `width`, `height`, `framesIn`, `framesOut`, `colors`, `format`,
  `passthrough`.
- `GET /health` — 200 OK, for probes.

Errors return JSON `{"error": "..."}` with status 400 (bad mode or
caps), 413 (over 32 MB), 422 (undecodable/unsupported input, input
over the decode budgets, or a conversion that missed the server
deadline) or 503 (both conversion slots busy).

## Limits

- Decode budgets (before any allocation): input at most 4096 px per
  side, 4M pixels per frame, 1000 frames, 64M total pixels. The GIF
  logical screen is four attacker-picked header bytes, so dims are
  validated from the header before the frames decode.
- Server-side: conversions run at most 2 at a time and are bounded by
  a 30 s deadline; the context cancels compositing, resizing, probing
  and quantizing at their frame boundaries. The browser adds its own
  20 s timeout.

Input already within the limits is returned byte-for-byte, unless a
cap would change the output.

## Configuration

- `--port` / `PORT` — listen port (default `3000`).

## Notes

- The UI follows the system colour scheme (light and dark).
- The UI style was dealt with tasteskill (app screen, achromatic-ledger
  style, seed 2121207780).
