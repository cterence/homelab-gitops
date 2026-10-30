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

	_ "image/jpeg"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	maxBytes  = 128 * 1024
	maxDim    = 128
	maxUpload = 32 << 20

	ModeRatio  = "ratio"
	ModeSquare = "square"
)

// paletteLadder is tried high to low: quality is dropped before frames.
var paletteLadder = [...]int{256, 128, 64, 32, 16, 8, 4, 2}

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

// Convert adapts data (gif, png, jpeg or webp) to Slack emoji limits:
// at most 128KB and 128x128px. All frames are kept; palette quality is
// reduced first and frames are thinned only as a last resort. Input that
// already fits is returned byte-for-byte.
func Convert(data []byte, mode string) ([]byte, *Report, error) {
	switch ct := http.DetectContentType(data); ct {
	case "image/gif":
		return convertGIF(data, mode, ct)
	case "image/png", "image/jpeg", "image/webp":
		return convertStill(data, mode, ct)
	default:
		return nil, nil, fmt.Errorf("unsupported content type %q: want gif, png, jpeg or webp", ct)
	}
}

func convertGIF(data []byte, mode, ct string) ([]byte, *Report, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("decoding gif: %w", err)
	}

	report := &Report{
		InputSize: len(data),
		FramesIn:  len(g.Image),
		Format:    ct,
	}

	if len(data) <= maxBytes && g.Config.Width <= maxDim && g.Config.Height <= maxDim {
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

	return finish(frames, delays, g.Config.Width, g.Config.Height, mode, g.LoopCount, report)
}

func convertStill(data []byte, mode, ct string) ([]byte, *Report, error) {
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

	if len(data) <= maxBytes && b.Dx() <= maxDim && b.Dy() <= maxDim {
		report.Passthrough = true
		report.OutputSize = len(data)
		report.Width, report.Height = b.Dx(), b.Dy()

		return data, report, nil
	}

	frame := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(frame, frame.Bounds(), img, b.Min, draw.Src)

	return finish([]*image.NRGBA{frame}, []int{0}, b.Dx(), b.Dy(), mode, 0, report)
}

func finish(
	frames []*image.NRGBA,
	delays []int,
	w, h int,
	mode string,
	loopCount int,
	report *Report,
) ([]byte, *Report, error) {
	tw, th := targetSize(w, h, mode)

	small := make([]*image.NRGBA, len(frames))

	for i, f := range frames {
		small[i] = resizeFrame(f, tw, th)
	}

	out, colors, framesOut, err := fitBudget(small, delays, loopCount)
	if err != nil {
		return nil, nil, err
	}

	report.OutputSize = len(out)
	report.Width, report.Height = tw, th
	report.FramesOut = framesOut
	report.Colors = colors

	return out, report, nil
}

// targetSize returns the output dimensions for the given mode. Square mode
// always stretches to the limit; ratio mode never upscales.
func targetSize(w, h int, mode string) (int, int) {
	if mode == ModeSquare {
		return maxDim, maxDim
	}

	if w <= maxDim && h <= maxDim {
		return w, h
	}

	s := float64(maxDim) / float64(max(w, h))

	return max(1, int(math.Round(float64(w)*s))), max(1, int(math.Round(float64(h)*s)))
}

// fitBudget encodes frames under maxBytes. Palette quality is exhausted
// (256 down to 2 colors) at every frame count before any frame is dropped.
func fitBudget(frames []*image.NRGBA, delays []int, loopCount int) ([]byte, int, int, error) {
	for keep := len(frames); keep >= 1; keep /= 2 {
		kf, kd := thinFrames(frames, delays, keep)

		for _, colors := range paletteLadder {
			pal := quantizeFrames(kf, colors)

			out, err := encodeGIF(pal, kd, loopCount)
			if err != nil {
				return nil, 0, 0, err
			}

			if len(out) <= maxBytes {
				return out, colors, len(kf), nil
			}
		}
	}

	return nil, 0, 0, fmt.Errorf("cannot fit under %d bytes even at 2 colors and 1 frame", maxBytes)
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
