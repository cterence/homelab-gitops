package main

import (
	"bytes"
	"fmt"
	"image"
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
)

// Options tune a conversion. Zero values mean "no manual adjustment".
type Options struct {
	Mode      string
	MaxFrames int // 0: keep every frame
	MaxSide   int // 0: 128px (the Slack limit)
	MaxColors int // 0: 255 (fill the budget)
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

func side(opts Options) int {
	if opts.MaxSide > 0 {
		return opts.MaxSide
	}

	return maxSide
}

// Convert adapts data (gif, png, jpeg or webp) to Slack emoji limits:
// at most 128KB and 128x128px. All frames are kept by default; palette
// quality is reduced first and frames are thinned only as a last resort,
// or earlier when Options caps them. Input that already fits (and no cap
// would change it) is returned byte-for-byte.
func Convert(data []byte, opts Options) ([]byte, *Report, error) {
	switch ct := http.DetectContentType(data); ct {
	case "image/gif":
		return convertGIF(data, opts, ct)
	case "image/png", "image/jpeg", "image/webp":
		return convertStill(data, opts, ct)
	default:
		return nil, nil, fmt.Errorf("unsupported content type %q: want gif, png, jpeg or webp", ct)
	}
}

func convertGIF(data []byte, opts Options, ct string) ([]byte, *Report, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("decoding gif: %w", err)
	}

	report := &Report{
		InputSize:   len(data),
		InputWidth:  g.Config.Width,
		InputHeight: g.Config.Height,
		FramesIn:    len(g.Image),
		Format:      ct,
	}

	s := side(opts)

	if len(data) <= maxBytes && g.Config.Width <= s && g.Config.Height <= s &&
		(opts.MaxFrames == 0 || opts.MaxFrames >= len(g.Image)) &&
		(opts.MaxColors <= 0 || opts.MaxColors >= 255) {
		report.Passthrough = true
		report.OutputSize = len(data)
		report.Width, report.Height = g.Config.Width, g.Config.Height
		report.FramesOut = len(g.Image)
		report.Colors = paletteSize(g)

		return data, report, nil
	}

	frames := compositeFrames(g)
	delays := make([]int, len(frames))

	for i := range delays {
		delays[i] = g.Delay[i]
	}

	return finish(frames, delays, g.Config.Width, g.Config.Height, opts, g.LoopCount, report)
}

func convertStill(data []byte, opts Options, ct string) ([]byte, *Report, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("decoding image: %w", err)
	}

	b := img.Bounds()

	report := &Report{
		InputSize:   len(data),
		InputWidth:  b.Dx(),
		InputHeight: b.Dy(),
		FramesIn:    1,
		Format:      ct,
	}

	s := side(opts)

	if len(data) <= maxBytes && b.Dx() <= s && b.Dy() <= s &&
		(opts.MaxColors <= 0 || opts.MaxColors >= 255) {
		report.Passthrough = true
		report.OutputSize = len(data)
		report.Width, report.Height = b.Dx(), b.Dy()

		return data, report, nil
	}

	frame := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(frame, frame.Bounds(), img, b.Min, draw.Src)

	return finish([]*image.NRGBA{frame}, []int{0}, b.Dx(), b.Dy(), opts, 0, report)
}

func finish(
	frames []*image.NRGBA,
	delays []int,
	w, h int,
	opts Options,
	loopCount int,
	report *Report,
) ([]byte, *Report, error) {
	sizes := sizeLadder(side(opts))

	// resize once per candidate size; thinning below picks subsets
	resized := make([][]*image.NRGBA, len(sizes))
	dims := make([][2]int, len(sizes))

	for i, s := range sizes {
		tw, th := targetSize(w, h, opts.Mode, s)
		dims[i] = [2]int{tw, th}

		r := make([]*image.NRGBA, len(frames))

		for j, f := range frames {
			r[j] = resizeFrame(f, tw, th)
		}

		resized[i] = r
	}

	startKeep := len(frames)

	if opts.MaxFrames > 0 && opts.MaxFrames < startKeep {
		startKeep = opts.MaxFrames
	}

	// attempt one (size, frame count) candidate; ok=false means the color
	// floor does not fit there
	ceil := 255

	if opts.MaxColors > 0 && opts.MaxColors < ceil {
		ceil = opts.MaxColors
	}

	try := func(sizeIdx, keep int) (out []byte, colors, kept int, ok bool, err error) {
		kf, kd := thinFrames(resized[sizeIdx], delays, keep)

		out, colors, err = searchPalette(kf, kd, loopCount, ceil)
		if err != nil || out == nil {
			return nil, 0, 0, false, err
		}

		return out, colors, len(kf), true, nil
	}

	// interleaved ladder: one frame halving inside each size step before
	// the resolution drops, so (64px, half frames) beats (48px, all
	// frames) - a smaller size is not worth more motion
	for i := range sizes {
		for _, keep := range [...]int{startKeep, startKeep / 2} {
			if keep < 1 {
				continue
			}

			out, colors, kept, ok, err := try(i, keep)
			if err != nil {
				return nil, nil, err
			}

			if !ok {
				continue
			}

			report.OutputSize = len(out)
			report.Width, report.Height = dims[i][0], dims[i][1]
			report.FramesOut = kept
			report.Colors = colors

			return out, report, nil
		}
	}

	// resolution exhausted: keep halving frames at the smallest size
	for keep := startKeep / 4; keep >= 1; keep /= 2 {
		last := len(sizes) - 1

		out, colors, kept, ok, err := try(last, keep)
		if err != nil {
			return nil, nil, err
		}

		if !ok {
			continue
		}

		report.OutputSize = len(out)
		report.Width, report.Height = dims[last][0], dims[last][1]
		report.FramesOut = kept
		report.Colors = colors

		return out, report, nil
	}

	return nil, nil, fmt.Errorf(
		"cannot fit under %d bytes even at %d colors, %d px and 1 frame",
		maxBytes, colorFloor, sizes[len(sizes)-1])
}

// sizeLadder is the degradation order for resolution: the user's side
// limit first, then the steps below it, never above.
func sizeLadder(maxSide int) []int {
	all := [...]int{128, 96, 64, 48}

	var out []int

	for _, s := range all {
		if s <= maxSide {
			out = append(out, s)
		}
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
	frames []*image.NRGBA,
	delays []int,
	loopCount int,
	hi int,
) ([]byte, int, error) {
	loOut, err := encodeGIF(quantizeFrames(frames, colorFloor, true), delays, loopCount)
	if err != nil {
		return nil, 0, err
	}

	if len(loOut) > maxBytes {
		return nil, 0, nil
	}

	lo, loColors := colorFloor, colorFloor

	for hi > lo {
		mid := (lo + hi + 1) / 2

		out, err := encodeGIF(quantizeFrames(frames, mid, true), delays, loopCount)
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
