package main

import (
	"image"
	"image/color"
	"slices"
)

const (
	alphaThreshold = 0x80
	// maxSamples caps how many pixels median cut inspects; enough for a
	// 128x128 canvas many times over.
	maxSamples = 1 << 16
)

// quantizeFrames converts frames to paletted images sharing one palette
// of at most maxColors entries (one slot is spent on transparency when the
// frames have transparent pixels). Median cut picks the palette.
func quantizeFrames(frames []*image.NRGBA, maxColors int) []*image.Paletted {
	var (
		pixels         []color.RGBA
		hasTransparent bool
		total          int
	)

	for _, f := range frames {
		b := f.Bounds()
		total += b.Dx() * b.Dy()
	}

	stride := max(1, total/maxSamples)

	var i int

	for _, f := range frames {
		b := f.Bounds()

		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := f.NRGBAAt(x, y)
				i++

				if c.A < alphaThreshold {
					hasTransparent = true
					continue
				}

				if i%stride != 0 {
					continue
				}

				pixels = append(pixels, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
			}
		}
	}

	budget := maxColors

	if hasTransparent {
		budget--
	}

	opaque := medianCut(pixels, budget)

	transparentIndex := 0

	if hasTransparent {
		transparentIndex = 1
	}

	pal := make(color.Palette, 0, maxColors)

	if hasTransparent {
		pal = append(pal, color.RGBA{})
	}

	pal = append(pal, opaque...)

	out := make([]*image.Paletted, len(frames))

	for i, f := range frames {
		b := f.Bounds()
		p := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), pal)

		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := f.NRGBAAt(x, y)

				if c.A < alphaThreshold {
					p.SetColorIndex(x-b.Min.X, y-b.Min.Y, 0)
					continue
				}

				p.SetColorIndex(x-b.Min.X, y-b.Min.Y, uint8(int(nearestIndex(opaque, c))+transparentIndex))
			}
		}

		out[i] = p
	}

	return out
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
