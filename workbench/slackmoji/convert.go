package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	maxBytes  = 128 * 1024
	maxSide   = 128
	minSide   = 32
	maxUpload = 32 << 20

	ModeRatio  = "ratio"
	ModeSquare = "square"

	// colorFloor: below this, dithering cannot hide posterization, so the
	// pipeline degrades size (then frames) instead of going lower.
	colorFloor = 16

	// Input budgets: the GIF screen is four attacker-picked header bytes
	// and pixel data decompresses, so decode work is bounded before any
	// allocation. Slack emoji output is 128px; larger input is wasted.
	maxInputSide    = 4096
	maxInputPixels  = 4 << 20
	maxInputFrames  = 1000
	maxInputTotalPx = 64 << 20
)

// Options tune a conversion. Zero values mean "no manual adjustment".
type Options struct {
	Mode string
	// Pins hold an axis at exactly this value; the free axes degrade to
	// pay for it. Zero means the axis is free and sits at the optimum.
	PinSide   int // px
	PinFrames int // frame count
	PinColors int // palette size
	// Priority names the axis whose pin yields first when the pinned
	// set cannot fit: "side", "frames" or "colors" ("" = no fallback).
	Priority string
}

// Report describes what the conversion did to the input.
type Report struct {
	Passthrough bool   `json:"passthrough"`
	InputSize   int    `json:"inputSize"`
	InputWidth  int    `json:"inputWidth"`
	InputHeight int    `json:"inputHeight"`
	OutputSize  int    `json:"outputSize"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FramesIn    int    `json:"framesIn"`
	FramesOut   int    `json:"framesOut"`
	Colors      int    `json:"colors"`
	Format      string `json:"format"`
}

// fitsBudget reports whether input already satisfies every limit,
// including the user's caps.
func fitsBudget(dataLen, w, h, frames int, opts Options) bool {
	s := maxSide

	return dataLen <= maxBytes && w <= s && h <= s &&
		opts.PinSide == 0 && opts.PinFrames == 0 && opts.PinColors == 0
}

// passthroughReport fills a Report for input returned byte-for-byte, so
// every path emits the same contract.
func passthroughReport(r *Report, dataLen, w, h, frames, colors int) {
	r.Passthrough = true
	r.OutputSize = dataLen
	r.Width, r.Height = w, h
	r.FramesOut = frames
	r.Colors = colors
}

// colorCount counts distinct opaque colors of a small image.
func colorCount(img image.Image) int {
	b := img.Bounds()

	seen := make(map[color.RGBA]struct{})

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cr, cg, cb, ca := img.At(x, y).RGBA()

			if ca < alphaThreshold<<8 {
				continue
			}

			seen[color.RGBA{R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8), A: 255}] = struct{}{}
		}
	}

	return len(seen)
}

// checkDims bounds the decode before any allocation. The GIF logical
// screen comes from four header bytes; the still path reads the header
// only, so oversized input never materializes.
func checkDims(w, h int) error {
	if w > maxInputSide || h > maxInputSide || w <= 0 || h <= 0 {
		return fmt.Errorf("input %dx%d exceeds the %dpx per-side limit", w, h, maxInputSide)
	}

	if w*h > maxInputPixels {
		return fmt.Errorf("input %dx%d (%d pixels) exceeds the %d-pixel limit",
			w, h, w*h, maxInputPixels)
	}

	return nil
}

// checkInput bounds the compositing work: canvas plus a clone per frame.
func checkInput(w, h, frames int) error {
	if err := checkDims(w, h); err != nil {
		return err
	}

	if frames > maxInputFrames {
		return fmt.Errorf("input has %d frames, over the %d-frame limit", frames, maxInputFrames)
	}

	if w*h*frames > maxInputTotalPx {
		return fmt.Errorf("input is %d pixels over %d frames, over the %d total-pixel limit",
			w*h*frames, frames, maxInputTotalPx)
	}

	return nil
}

// Convert adapts data (gif, png, jpeg or webp) to Slack emoji limits:
// at most 128KB and 128x128px. All frames are kept by default; palette
// quality is reduced first and frames are thinned only as a last resort,
// or earlier when Options pins them. Input that already fits (and no pin
// would change it) is returned byte-for-byte. The context bounds the
// conversion: it stops at the next frame or encode boundary when done.
// normalizePins clears pins that ask for what the input already is, so
// compliant input still passes through untouched.
func normalizePins(opts *Options, frames, w, h int) {
	if opts.PinFrames >= frames {
		opts.PinFrames = 0
	}

	if opts.PinColors >= 255 {
		opts.PinColors = 0
	}

	// ratio mode never upscales: a pin at or above the input side cannot
	// change anything there. Square mode holds every pin exactly - 128
	// means stretch to exactly 128, not "uncapped".
	if opts.Mode == ModeRatio && opts.PinSide >= max(w, h) {
		opts.PinSide = 0
	}

	switch opts.Priority {
	case "side":
		if opts.PinSide == 0 {
			opts.Priority = ""
		}
	case "frames":
		if opts.PinFrames == 0 {
			opts.Priority = ""
		}
	case "colors":
		if opts.PinColors == 0 {
			opts.Priority = ""
		}
	default:
		opts.Priority = ""
	}
}

func Convert(ctx context.Context, data []byte, opts Options) ([]byte, *Report, error) {
	switch ct := http.DetectContentType(data); ct {
	case "image/gif":
		return convertGIF(ctx, data, opts, ct)
	case "image/png", "image/jpeg", "image/webp":
		return convertStill(ctx, data, opts, ct)
	default:
		return nil, nil, fmt.Errorf("unsupported content type %q: want gif, png, jpeg or webp", ct)
	}
}

func convertGIF(ctx context.Context, data []byte, opts Options, ct string) ([]byte, *Report, error) {
	if cfg, err := gif.DecodeConfig(bytes.NewReader(data)); err == nil {
		if err := checkDims(cfg.Width, cfg.Height); err != nil {
			return nil, nil, err
		}
	}

	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("decoding gif: %w", err)
	}

	if err := checkInput(g.Config.Width, g.Config.Height, len(g.Image)); err != nil {
		return nil, nil, err
	}

	normalizePins(&opts, len(g.Image), g.Config.Width, g.Config.Height)

	report := &Report{
		InputSize:   len(data),
		InputWidth:  g.Config.Width,
		InputHeight: g.Config.Height,
		FramesIn:    len(g.Image),
		Format:      ct,
	}

	if fitsBudget(len(data), g.Config.Width, g.Config.Height, len(g.Image), opts) {
		passthroughReport(report, len(data), g.Config.Width, g.Config.Height,
			len(g.Image), paletteSize(g))

		return data, report, nil
	}

	frames, err := compositeFrames(ctx, g)
	if err != nil {
		return nil, nil, err
	}

	delays := make([]int, len(frames))

	for i := range delays {
		delays[i] = g.Delay[i]
	}

	return finish(ctx, frames, delays, g.Config.Width, g.Config.Height, opts, g.LoopCount, report)
}

func convertStill(ctx context.Context, data []byte, opts Options, ct string) ([]byte, *Report, error) {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if err := checkDims(cfg.Width, cfg.Height); err != nil {
			return nil, nil, err
		}
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("decoding image: %w", err)
	}

	b := img.Bounds()

	if err := checkInput(b.Dx(), b.Dy(), 1); err != nil {
		return nil, nil, err
	}

	normalizePins(&opts, 1, b.Dx(), b.Dy())

	report := &Report{
		InputSize:   len(data),
		InputWidth:  b.Dx(),
		InputHeight: b.Dy(),
		FramesIn:    1,
		Format:      ct,
	}

	if fitsBudget(len(data), b.Dx(), b.Dy(), 1, opts) {
		passthroughReport(report, len(data), b.Dx(), b.Dy(), 1, colorCount(img))

		return data, report, nil
	}

	frame := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(frame, frame.Bounds(), img, b.Min, draw.Src)

	return finish(ctx, []*image.NRGBA{frame}, []int{0}, b.Dx(), b.Dy(), opts, 0, report)
}

func finish(
	ctx context.Context,
	frames []*image.NRGBA,
	delays []int,
	w, h int,
	opts Options,
	loopCount int,
	report *Report,
) ([]byte, *Report, error) {
	// the pinned set is tried as asked; if it cannot fit, the axis the
	// user dragged last yields first, then the pins are impossible
	attempts := []Options{opts}

	if opts.Priority != "" {
		relaxed := opts

		switch opts.Priority {
		case "side":
			relaxed.PinSide = 0
		case "frames":
			relaxed.PinFrames = 0
		case "colors":
			relaxed.PinColors = 0
		}

		relaxed.Priority = ""
		attempts = append(attempts, relaxed)
	}

	for _, o := range attempts {
		out, rep, err := ladder(ctx, frames, delays, w, h, o, loopCount, report)
		if err != nil || out != nil {
			return out, rep, err
		}
	}

	return nil, nil, fmt.Errorf("the pinned values cannot fit under %d bytes", maxBytes)
}

// ladder walks the degradation order under the pins: pinned axes hold
// exactly their value while free axes sit at the best point the budget
// allows. A nil result means nothing fits.
func ladder(
	ctx context.Context,
	frames []*image.NRGBA,
	delays []int,
	w, h int,
	opts Options,
	loopCount int,
	report *Report,
) ([]byte, *Report, error) {
	sizes := sizeLadder(maxSide)

	if opts.PinSide > 0 {
		sizes = []int{opts.PinSide}
	} else if in := max(w, h); in < sizes[len(sizes)-1] {
		// a small input never benefits from invented pixels: its own
		// size is the last step of the ladder
		sizes = append(sizes, in)
	}

	// resize once per candidate size; thinning below picks subsets.
	// Ratio mode never upscales, so several steps can mean the same
	// output size - encode each distinct size only once.
	resized := make([][]*image.NRGBA, 0, len(sizes))
	dims := make([][2]int, 0, len(sizes))

	for _, s := range sizes {
		tw, th := targetSize(w, h, opts.Mode, s)

		if len(dims) > 0 && dims[len(dims)-1] == [2]int{tw, th} {
			continue
		}

		dims = append(dims, [2]int{tw, th})

		r := make([]*image.NRGBA, len(frames))

		for j, f := range frames {
			if err := ctx.Err(); err != nil {
				return nil, nil, fmt.Errorf("converting: %w", err)
			}

			r[j] = resizeFrame(f, tw, th)
		}

		resized = append(resized, r)
	}

	candidates := len(resized)

	startKeep := len(frames)
	pinnedKeep := false

	if opts.PinFrames > 0 {
		startKeep = opts.PinFrames
		pinnedKeep = true
	}

	// attempt a (size, frame count) candidate with a full palette search
	floor, ceil := colorFloor, 255

	if opts.PinColors > 0 {
		floor, ceil = opts.PinColors, opts.PinColors
	}

	accept := func(sizeIdx, keep int) ([]byte, *Report, error) {
		kf, kd := thinFrames(resized[sizeIdx], delays, keep)

		out, colors, err := searchPalette(ctx, kf, kd, loopCount, floor, ceil)
		if err != nil {
			return nil, nil, err
		}

		report.OutputSize = len(out)
		report.Width, report.Height = dims[sizeIdx][0], dims[sizeIdx][1]
		report.FramesOut = len(kf)
		report.Colors = colors

		return out, report, nil
	}

	// does the color floor fit at this (size, frame count)?
	floorFits := func(sizeIdx, keep int) (bool, error) {
		kf, kd := thinFrames(resized[sizeIdx], delays, keep)

		pal, disposal, err := quantizeFrames(ctx, kf, floor)
		if err != nil {
			return false, err
		}

		out, err := encodeGIF(pal, disposal, kd, loopCount)
		if err != nil {
			return false, err
		}

		return len(out) <= maxBytes, nil
	}

	// the largest keep in [lo, hi] whose floor encoding fits; lo must fit
	maxKeep := func(sizeIdx, lo, hi int) (int, error) {
		for lo < hi {
			mid := (lo + hi + 1) / 2

			fits, err := floorFits(sizeIdx, mid)
			if err != nil {
				return 0, err
			}

			if fits {
				lo = mid
			} else {
				hi = mid - 1
			}
		}

		return lo, nil
	}

	half := startKeep / 2

	// the ladder as data: per size, the full frame request, then the
	// largest count between half and the full request - the count is
	// searched, not halved, so a 75% request never silently becomes 37%.
	// After the sizes are exhausted, the smallest size keeps as many
	// frames as it can hold.
	type probe struct{ size, lo, hi int }

	probes := make([]probe, 0, 2*candidates+1)

	for i := range candidates {
		probes = append(probes, probe{i, startKeep, startKeep})

		if !pinnedKeep && half >= 1 {
			probes = append(probes, probe{i, half, startKeep})
		}
	}

	if !pinnedKeep && half >= 1 {
		probes = append(probes, probe{candidates - 1, 1, half})
	}

	for _, p := range probes {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("converting: %w", err)
		}

		fits, err := floorFits(p.size, p.lo)
		if err != nil {
			return nil, nil, err
		}

		if !fits {
			continue
		}

		keep, err := maxKeep(p.size, p.lo, p.hi)
		if err != nil {
			return nil, nil, err
		}

		return accept(p.size, keep)
	}

	return nil, nil, nil
}

// sizeLadder is the degradation order for resolution: the user's side
// limit first, then the steps below it, never above. A limit below the
// smallest step stands on its own.
func sizeLadder(maxSide int) []int {
	all := [...]int{128, 96, 64, 48}

	var out []int

	for _, s := range all {
		if s <= maxSide {
			out = append(out, s)
		}
	}

	if len(out) == 0 {
		out = []int{maxSide}
	}

	return out
}

// targetSize returns the output dimensions for the given mode and side
// limit. Square mode always stretches to the limit; ratio mode never
// upscales.
func targetSize(w, h int, mode string, s int) (int, int) {
	if mode == ModeSquare {
		return s, s
	}

	if w <= s && h <= s {
		return w, h
	}

	scale := float64(s) / float64(max(w, h))

	return max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
}

// searchPalette binary-searches the largest palette (colorFloor..hi)
// whose dithered encoding fits the budget. Returns nil when even the
// color floor does not fit.
func searchPalette(
	ctx context.Context,
	frames []*image.NRGBA,
	delays []int,
	loopCount int,
	floor int,
	hi int,
) ([]byte, int, error) {
	loPal, loDisposal, err := quantizeFrames(ctx, frames, floor)
	if err != nil {
		return nil, 0, err
	}

	loOut, err := encodeGIF(loPal, loDisposal, delays, loopCount)
	if err != nil {
		return nil, 0, err
	}

	if len(loOut) > maxBytes {
		return nil, 0, nil
	}

	lo, loColors := floor, floor

	for hi > lo {
		mid := (lo + hi + 1) / 2

		if err := ctx.Err(); err != nil {
			return nil, 0, fmt.Errorf("converting: %w", err)
		}

		pal, disposal, err := quantizeFrames(ctx, frames, mid)
		if err != nil {
			return nil, 0, err
		}

		out, err := encodeGIF(pal, disposal, delays, loopCount)
		if err != nil {
			return nil, 0, err
		}

		if len(out) > maxBytes {
			hi = mid - 1
			continue
		}

		lo, loOut, loColors = mid, out, mid
	}

	return loOut, loColors, nil
}

// thinFrames keeps the first and last frame plus evenly spread frames,
// merging the delays of dropped frames into the kept one before them so
// the total duration is preserved.
func thinFrames(frames []*image.NRGBA, delays []int, keep int) ([]*image.NRGBA, []int) {
	if keep >= len(frames) {
		return frames, delays
	}

	n := len(frames)
	keep = max(keep, 1)

	idx := make([]int, keep)

	for j := range keep {
		if keep == 1 {
			idx[j] = 0
		} else {
			idx[j] = j * (n - 1) / (keep - 1)
		}
	}

	kf := make([]*image.NRGBA, 0, keep)
	kd := make([]int, 0, keep)

	for j, i := range idx {
		kf = append(kf, frames[i])

		end := n

		if j+1 < len(idx) {
			end = idx[j+1]
		}

		d := 0

		for _, x := range delays[i:end] {
			d += x
		}

		kd = append(kd, d)
	}

	return kf, kd
}

func resizeFrame(src image.Image, w, h int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	return dst
}
