package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"slices"
)

const (
	alphaThreshold = 0x80
	// maxSamples caps how many pixels median cut inspects.
	maxSamples = 1 << 16
)

// bayer is the classic 8x8 ordered-dither matrix (values 0-63).
var bayer = [8][8]int{
	{0, 32, 8, 40, 2, 34, 10, 42},
	{48, 16, 56, 24, 50, 18, 58, 26},
	{12, 44, 4, 36, 14, 46, 6, 38},
	{60, 28, 52, 20, 62, 30, 54, 22},
	{3, 35, 11, 43, 1, 33, 9, 41},
	{51, 19, 59, 27, 49, 17, 57, 25},
	{15, 47, 7, 39, 13, 45, 5, 37},
	{63, 31, 55, 23, 61, 29, 53, 21},
}

// quantizeFrames converts frames to paletted images sharing one global
// palette: maxColors visible colors plus one transparent entry (index 0),
// which also marks unchanged pixels. Every frame after the first stores
// only its pixels that differ from the previous frame (gifsicle -O3
// style), which makes mostly-static animations far smaller. The palette
// is built from frame 0 plus the pixels that change between frames
// (ffmpeg palettegen stats_mode=diff style) and the mapping is
// ordered-dithered so low color counts read as gradients instead of bands.
//
// A transparent index means "keep the underlying pixel", so a gif whose
// frames punch holes (a subject moving over transparency) cannot express
// "become transparent" in delta mode: those gifs get disposal background
// and a full repaint of every opaque pixel instead, so holes really clear.
func quantizeFrames(ctx context.Context, frames []*image.NRGBA, maxColors int) ([]*image.Paletted, []byte, error) {
	// the transparent entry is extra, so visible colors cap at 255
	visible := min(max(1, maxColors), 255)
	pixels := diffSample(frames)
	opaque := medianCut(pixels, visible)

	pal := make(color.Palette, 0, maxColors+1)
	pal = append(pal, color.RGBA{})
	pal = append(pal, opaque...)

	clears := hasHoles(frames)
	disposal := make([]byte, len(frames))

	if clears {
		for i := range disposal {
			disposal[i] = gif.DisposalBackground
		}
	}

	out := make([]*image.Paletted, len(frames))

	for i, f := range frames {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("quantizing: %w", err)
		}

		b := f.Bounds()
		p := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), pal)

		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := f.NRGBAAt(x, y)

				px, py := x-b.Min.X, y-b.Min.Y

				if c.A < alphaThreshold {
					p.SetColorIndex(px, py, 0)
					continue
				}

				// delta: identical to the previous frame means the
				// previous frame already shows this pixel. After a
				// clear there is no underlying pixel to keep.
				if !clears && i > 0 && unchanged(frames[i-1].NRGBAAt(x, y), c) {
					p.SetColorIndex(px, py, 0)
					continue
				}

				off := int((float64(bayer[py&7][px&7])/63.5 - 0.5) * 64)
				c.R = clampU8(int(c.R) + off)
				c.G = clampU8(int(c.G) + off)
				c.B = clampU8(int(c.B) + off)

				p.SetColorIndex(px, py, nearestIndex(opaque, c)+1)
			}
		}

		out[i] = p
	}

	return out, disposal, nil
}

// hasHoles reports whether any frame turns transparent a pixel that the
// previously displayed state showed opaque, the loop wrap included.
func hasHoles(frames []*image.NRGBA) bool {
	for i := 1; i < len(frames); i++ {
		if frameHasHoles(frames[i-1], frames[i]) {
			return true
		}
	}

	return len(frames) > 1 && frameHasHoles(frames[len(frames)-1], frames[0])
}

func frameHasHoles(prev, cur *image.NRGBA) bool {
	b := cur.Bounds()

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if prev.NRGBAAt(x, y).A >= alphaThreshold && cur.NRGBAAt(x, y).A < alphaThreshold {
				return true
			}
		}
	}

	return false
}

func unchanged(prev, cur color.NRGBA) bool {
	return prev == cur
}

// diffSample collects the pixels that carry motion: every pixel of the
// first frame plus the pixels where consecutive frames differ.
func diffSample(frames []*image.NRGBA) []color.RGBA {
	total := 0

	for _, f := range frames {
		b := f.Bounds()
		total += b.Dx() * b.Dy()
	}

	stride := max(1, total/maxSamples)

	var (
		pixels []color.RGBA
		n      int
	)

	for i, f := range frames {
		b := f.Bounds()

		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := f.NRGBAAt(x, y)
				n++

				if i > 0 && unchanged(frames[i-1].NRGBAAt(x, y), c) {
					continue
				}

				if c.A < alphaThreshold {
					continue
				}

				if n%stride != 0 {
					continue
				}

				pixels = append(pixels, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
			}
		}
	}

	return pixels
}

func clampU8(n int) uint8 {
	return uint8(min(255, max(0, n)))
}

// medianCut splits the pixel set into at most n boxes along the widest
// channel and averages each box into one palette color.
func medianCut(pixels []color.RGBA, n int) color.Palette {
	if len(pixels) == 0 || n < 1 {
		return nil
	}

	boxes := [][]color.RGBA{pixels}

	for len(boxes) < n {
		bi, ch := widestBox(boxes)

		if bi < 0 {
			break
		}

		box := boxes[bi]

		slices.SortFunc(box, func(a, b color.RGBA) int {
			return int(channel(a, ch)) - int(channel(b, ch))
		})

		mid := len(box) / 2

		boxes[bi] = box[:mid]
		boxes = append(boxes, box[mid:])
	}

	pal := make(color.Palette, 0, len(boxes))

	for _, box := range boxes {
		var r, g, b uint32

		for _, c := range box {
			r += uint32(c.R)
			g += uint32(c.G)
			b += uint32(c.B)
		}

		m := uint32(len(box))
		pal = append(pal, color.RGBA{R: uint8(r / m), G: uint8(g / m), B: uint8(b / m), A: 255})
	}

	return pal
}

// widestBox picks the box with the largest channel extent and returns its
// widest channel, or -1 when no box can be split further.
func widestBox(boxes [][]color.RGBA) (int, int) {
	bi, best, bestCh := -1, 1, 0

	for i, box := range boxes {
		if len(box) < 2 {
			continue
		}

		var rmin, rmax, gmin, gmax, bmin, bmax uint8

		rmin, gmin, bmin = 255, 255, 255

		for _, c := range box {
			rmin, rmax = min(rmin, c.R), max(rmax, c.R)
			gmin, gmax = min(gmin, c.G), max(gmax, c.G)
			bmin, bmax = min(bmin, c.B), max(bmax, c.B)
		}

		ch := 0

		switch g := int(gmax - gmin); {
		case int(rmax-rmin) >= g && int(rmax-rmin) >= int(bmax-bmin):
			ch = 0
		case g >= int(bmax-bmin):
			ch = 1
		default:
			ch = 2
		}

		var ext uint8

		switch ch {
		case 0:
			ext = rmax - rmin
		case 1:
			ext = gmax - gmin
		default:
			ext = bmax - bmin
		}

		// ext >= best with best starting at 1 skips degenerate boxes.
		if int(ext) >= best {
			bi, best, bestCh = i, int(ext), ch
		}
	}

	return bi, bestCh
}

func channel(c color.RGBA, ch int) uint8 {
	switch ch {
	case 0:
		return c.R
	case 1:
		return c.G
	default:
		return c.B
	}
}

func nearestIndex(pal color.Palette, c color.NRGBA) uint8 {
	best, bestD := 0, 1<<62

	for i, p := range pal {
		r, g, b, _ := p.RGBA()
		d := sq(int(r>>8)-int(c.R)) + sq(int(g>>8)-int(c.G)) + sq(int(b>>8)-int(c.B))

		if d < bestD {
			best, bestD = i, d
		}
	}

	return uint8(best)
}

func sq(n int) int {
	return n * n
}
