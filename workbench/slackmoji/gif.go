package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"

	"golang.org/x/image/draw"
)

// compositeFrames renders every gif frame onto a full-canvas image,
// honoring disposal methods, so each output frame stands alone.
func compositeFrames(g *gif.GIF) []*image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	frames := make([]*image.NRGBA, len(g.Image))

	var (
		saved    *image.NRGBA
		prevRect image.Rectangle
	)

	for i, fr := range g.Image {
		if i > 0 {
			switch g.Disposal[i-1] {
			case gif.DisposalBackground:
				clearRect(canvas, prevRect)
			case gif.DisposalPrevious:
				if saved != nil {
					canvas = saved
					saved = nil
				}
			}
		}

		if g.Disposal[i] == gif.DisposalPrevious {
			saved = cloneNRGBA(canvas)
		}

		// GIF frames replace their rect, but palette-transparent pixels
		// leave the underlying pixel untouched (that is how the delta
		// encoding round-trips), which is draw.Over for paletted sources.
		draw.Draw(canvas, fr.Rect, fr, fr.Rect.Min, draw.Over)
		prevRect = fr.Rect

		frames[i] = cloneNRGBA(canvas)
	}

	return frames
}

func clearRect(img *image.NRGBA, r image.Rectangle) {
	r = r.Intersect(img.Bounds())

	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, color.NRGBA{})
		}
	}
}

func cloneNRGBA(img *image.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, img.Bounds(), img, img.Bounds().Min, draw.Src)

	return out
}

func encodeGIF(frames []*image.Paletted, delays []int, loopCount int) ([]byte, error) {
	g := &gif.GIF{
		Image:     frames,
		Delay:     delays,
		LoopCount: loopCount,
		Config: image.Config{
			ColorModel: frames[0].Palette,
			Width:      frames[0].Bounds().Dx(),
			Height:     frames[0].Bounds().Dy(),
		},
	}

	var buf bytes.Buffer

	if err := gif.EncodeAll(&buf, g); err != nil {
		return nil, fmt.Errorf("encoding gif: %w", err)
	}

	return buf.Bytes(), nil
}

func paletteSize(g *gif.GIF) int {
	if len(g.Image) == 0 {
		return 0
	}

	return len(g.Image[0].Palette)
}
