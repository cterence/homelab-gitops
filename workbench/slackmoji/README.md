# slackmoji

Small web app that adapts a GIF (or still image) to Slack emoji limits:
at most 128 KB and at most 128x128 px. By default it keeps every frame
and drops quality first; frames are thinned only as a last resort. Any
axis can instead be pinned (held exactly) and the free axes degrade to
pay for it.

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
   A transparent index in GIF means "keep the underlying pixel", so a
   gif whose frames punch holes (a subject moving over transparency)
   cannot use delta encoding without ghosting: those gifs switch to
   disposal background with a full repaint of each frame's opaque
   pixels, which clears the holes; transparent areas compress to
   almost nothing, so the size cost is small.
   A pinned axis leaves this search where it is: size stays at the
   pinned side, frames at the pinned count, colors at the pinned
   palette, and only the free axes move. In square mode a pin at
   128 holds exactly 128 (it is not an uncapped ask).

## Endpoints

- `GET /` — web UI: one panel, a fixed frame that is a dropzone
  before upload and the preview after (click the preview to replace
  the file, Clear to go back to the empty dropzone), with the shape
  toggle and a slider column beside it (size, frames).
  The axes have a fixed priority: size first, then frames, then
  colors. The default size is the one that keeps the most frames;
  dragging the size slider locks it at the dragged value and
  re-optimizes frames under it, while frames drags hold the locked
  size. Colors are never adjusted: they are always the maximum the
  leftover budget buys, shown with the byte budget under the
  preview. The handle always shows the last outcome. Reset to
  optimal (and switching shape) unlocks everything back to the
  default. The size slider steps by 16 px and the frames slider in
  about sixteen bands anchored at the full count; the value under
  each dial always shows the true outcome, which may sit between
  steps.
- `POST /convert` — multipart form with `file`, `mode`
  (`ratio` | `square`) and the optional pins `frames` (1..1000,
  hold exactly this many frames), `side` (32-128 px, hold exactly
  this output side) and `colors` (16-255, hold exactly this palette
  size). Absent means the axis is free and sits at its optimum.
  `priority` (`frames` | `side` | `colors`) names the pin that yields
  first when the pinned set cannot fit under 128 KB, so an
  over-ambitious drag degrades that axis instead of failing.
  Returns JSON:
  `{"report": {...}, "data": "data:<mime>;base64,..."}` where `report`
  carries `inputSize`, `inputWidth`, `inputHeight`, `outputSize`,
  `width`, `height`, `framesIn`, `framesOut`, `colors`, `format`,
  `passthrough`.
- `GET /health` — 200 OK, for probes.

Errors return JSON `{"error": "..."}` with status 400 (bad mode,
pins or priority), 413 (over 32 MB), 422 (undecodable/unsupported
input, input over the decode budgets, a conversion that missed the
server deadline, or pins that cannot fit even after the priority
yielded) or 503 (both conversion slots busy).

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
pin would change the output.

## Configuration

- `--port` / `PORT` — listen port (default `3000`).

## Notes

- The UI follows the system colour scheme (light and dark).
- The UI style was dealt with tasteskill (app screen, achromatic-ledger
  style, seed 2121207780).
