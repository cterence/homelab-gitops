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
)

// Options tune a conversion. Zero values mean "no manual adjustment".
type Options struct {
	Mode      string
	MaxFrames int // 0: keep every frame
	MaxSide   int // 0: 128px (the Slack limit)
}

// Report describes what the conversion did to the input.
type Report struct {
	Passthrough bool   `json:"passthrough"`
	InputSize   int    `json:"inputSize"`
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
		InputSize: len(data),
		FramesIn:  len(g.Image),
		Format:    ct,
	}

	s := side(opts)

	if len(data) <= maxBytes && g.Config.Width <= s && g.Config.Height <= s &&
		(opts.MaxFrames == 0 || opts.MaxFrames >= len(g.Image)) {
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
		InputSize: len(data),
		FramesIn:  1,
		Format:    ct,
	}

	s := side(opts)

	if len(data) <= maxBytes && b.Dx() <= s && b.Dy() <= s {
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
	tw, th := targetSize(w, h, opts)

	small := make([]*image.NRGBA, len(frames))

	for i, f := range frames {
		small[i] = resizeFrame(f, tw, th)
	}

	out, colors, framesOut, err := fitBudget(small, delays, loopCount, opts.MaxFrames)
	if err != nil {
		return nil, nil, err
	}

	report.OutputSize = len(out)
	report.Width, report.Height = tw, th
	report.FramesOut = framesOut
	report.Colors = colors

	return out, report, nil
}

// targetSize returns the output dimensions for the given mode and side
// limit. Square mode always stretches to the limit; ratio mode never
// upscales.
func targetSize(w, h int, opts Options) (int, int) {
	s := side(opts)

	if opts.Mode == ModeSquare {
		return s, s
	}

	if w <= s && h <= s {
		return w, h
	}

	scale := float64(s) / float64(max(w, h))

	return max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
}

// fitBudget encodes frames under maxBytes. The frame count starts at the
// caller's cap (or every frame) and is halved as the last resort; at every
// frame count the palette is exhausted first (binary search from 256 down
// to 2 colors).
func fitBudget(frames []*image.NRGBA, delays []int, loopCount int, maxFrames int) ([]byte, int, int, error) {
	keep := len(frames)

	if maxFrames > 0 && maxFrames < keep {
		keep = maxFrames
	}

	for ; keep >= 1; keep /= 2 {
		kf, kd := thinFrames(frames, delays, keep)

		out, colors, err := searchPalette(kf, kd, loopCount)
		if err != nil {
			return nil, 0, 0, err
		}

		if out != nil {
			return out, colors, len(kf), nil
		}
	}

	return nil, 0, 0, fmt.Errorf("cannot fit under %d bytes even at 2 colors and 1 frame", maxBytes)
}

// searchPalette binary-searches the largest palette (2..256) whose
// encoding fits the budget, dithered first and undithered as a fallback.
// Returns nil when even 2 colors do not fit either way.
func searchPalette(
	frames []*image.NRGBA,
	delays []int,
	loopCount int,
) ([]byte, int, error) {
	for _, dither := range [...]bool{true, false} {
		loOut, err := encodeGIF(quantizeFrames(frames, 2, dither), delays, loopCount)
		if err != nil {
			return nil, 0, err
		}

		if len(loOut) > maxBytes {
			continue
		}

		lo, loColors := 2, 2

		for hi := 255; lo < hi; {
			mid := (lo + hi + 1) / 2

			out, err := encodeGIF(quantizeFrames(frames, mid, dither), delays, loopCount)
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

	return nil, 0, nil
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
