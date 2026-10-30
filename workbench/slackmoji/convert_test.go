package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math/rand/v2"
	"testing"
)

func solidGIF(t *testing.T, w, h, frames int) []byte {
	t.Helper()

	g := &gif.GIF{
		LoopCount: 0,
		Config:    image.Config{Width: w, Height: h},
	}

	colors := []color.RGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
		{B: 255, A: 255},
	}

	for i := range frames {
		pal := color.Palette{colors[i%len(colors)], color.RGBA{A: 255}}
		img := image.NewPaletted(image.Rect(0, 0, w, h), pal)

		for y := range h {
			for x := range w {
				img.SetColorIndex(x, y, 0)
			}
		}

		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encoding test gif: %v", err)
	}

	return buf.Bytes()
}

func noiseGIF(t *testing.T, w, h, frames int, seedA uint64) []byte {
	t.Helper()

	rng := rand.New(rand.NewPCG(seedA, 7))
	g := &gif.GIF{
		LoopCount: 0,
		Config:    image.Config{Width: w, Height: h},
	}

	for range frames {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))

		for y := range h {
			for x := range w {
				img.Set(x, y, color.NRGBA{
					R: uint8(rng.IntN(256)),
					G: uint8(rng.IntN(256)),
					B: uint8(rng.IntN(256)),
					A: 255,
				})
			}
		}

		pal := quantizeFrames([]*image.NRGBA{img}, 256)[0]

		g.Image = append(g.Image, pal)
		g.Delay = append(g.Delay, 4)
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encoding test gif: %v", err)
	}

	return buf.Bytes()
}

func TestTargetSize(t *testing.T) {
	tests := []struct {
		name         string
		w, h         int
		mode         string
		wantW, wantH int
	}{
		{"ratio landscape", 200, 100, ModeRatio, 128, 64},
		{"ratio portrait", 100, 200, ModeRatio, 64, 128},
		{"ratio already small", 64, 64, ModeRatio, 64, 64},
		{"ratio already exact", 128, 128, ModeRatio, 128, 128},
		{"ratio wide rounds", 300, 50, ModeRatio, 128, 21},
		{"ratio thin clamps", 400, 1, ModeRatio, 128, 1},
		{"square upscales", 100, 50, ModeSquare, 128, 128},
		{"square downscale", 200, 100, ModeSquare, 128, 128},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotW, gotH := targetSize(tt.w, tt.h, tt.mode)

			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("targetSize(%d, %d, %s) = %dx%d, want %dx%d",
					tt.w, tt.h, tt.mode, gotW, gotH, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestConvertPassthrough(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		mode string
	}{
		{"compliant gif", solidGIF(t, 64, 64, 3), ModeRatio},
		{"compliant gif square", solidGIF(t, 64, 64, 3), ModeSquare},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, report, err := Convert(tt.data, tt.mode)
			if err != nil {
				t.Fatalf("Convert() error = %v", err)
			}

			if !report.Passthrough {
				t.Error("expected passthrough")
			}

			if !bytes.Equal(out, tt.data) {
				t.Error("passthrough output differs from input")
			}
		})
	}
}

func TestConvertFitsBudgetKeepAllFrames(t *testing.T) {
	input := noiseGIF(t, 256, 256, 20, 1)

	out, report, err := Convert(input, ModeRatio)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.Passthrough {
		t.Fatal("expected re-encode, got passthrough")
	}

	if len(out) > maxBytes {
		t.Errorf("output %d bytes, want <= %d", len(out), maxBytes)
	}

	if report.Width > maxDim || report.Height > maxDim {
		t.Errorf("output %dx%d exceeds %dpx", report.Width, report.Height, maxDim)
	}

	if report.FramesOut != report.FramesIn {
		t.Errorf("frames %d -> %d, want all frames kept", report.FramesIn, report.FramesOut)
	}

	if _, err := gif.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("output is not a valid gif: %v", err)
	}
}

func TestConvertDropsFramesLast(t *testing.T) {
	input := noiseGIF(t, 128, 128, 120, 2)

	out, report, err := Convert(input, ModeRatio)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if len(out) > maxBytes {
		t.Errorf("output %d bytes, want <= %d", len(out), maxBytes)
	}

	if report.FramesOut >= report.FramesIn || report.FramesOut < 1 {
		t.Errorf("frames %d -> %d, want some dropped but at least one kept",
			report.FramesIn, report.FramesOut)
	}

	if report.FramesOut > 1 && report.Colors > 8 {
		t.Errorf("frames dropped but palette still at %d colors, want quality exhausted first", report.Colors)
	}
}

func TestThinFramesMergesDelays(t *testing.T) {
	frames := make([]*image.NRGBA, 4)
	delays := []int{10, 20, 30, 40}

	for i := range frames {
		frames[i] = image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}

	gotFrames, gotDelays := thinFrames(frames, delays, 2)

	if len(gotFrames) != 2 {
		t.Fatalf("kept %d frames, want 2", len(gotFrames))
	}

	// first and last frame kept: frame 0 absorbs 10+20+30, frame 3 keeps 40.
	want := []int{60, 40}

	for i, d := range gotDelays {
		if d != want[i] {
			t.Errorf("delay[%d] = %d, want %d", i, d, want[i])
		}
	}

	total := 0

	for _, d := range delays {
		total += d
	}

	gotTotal := 0

	for _, d := range gotDelays {
		gotTotal += d
	}

	if gotTotal != total {
		t.Errorf("total duration %d, want %d", gotTotal, total)
	}
}

func TestQuantizePaletteSize(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(1, 0, color.NRGBA{G: 255, A: 255})
	img.Set(2, 0, color.NRGBA{B: 255, A: 255})
	img.Set(3, 0, color.NRGBA{R: 128, A: 255})

	pal := quantizeFrames([]*image.NRGBA{img}, 2)[0]

	if got := len(pal.Palette); got > 2 {
		t.Errorf("palette has %d colors, want <= 2", got)
	}

	if pal.Bounds() != img.Bounds() {
		t.Errorf("bounds %v, want %v", pal.Bounds(), img.Bounds())
	}
}

func TestQuantizeTransparent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(1, 0, color.NRGBA{A: 0})

	pal := quantizeFrames([]*image.NRGBA{img}, 4)[0]

	_, _, _, a := pal.Palette[pal.ColorIndexAt(1, 0)].RGBA()
	if a != 0 {
		t.Error("transparent pixel mapped to opaque palette color")
	}
}

func TestConvertStillImage(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 200, 200))

	for y := range 200 {
		for x := range 200 {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), A: 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test png: %v", err)
	}

	out, report, err := Convert(buf.Bytes(), ModeRatio)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.Width > maxDim || report.Height > maxDim {
		t.Errorf("output %dx%d exceeds %dpx", report.Width, report.Height, maxDim)
	}

	if _, err := gif.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("output is not a valid gif: %v", err)
	}
}

func TestConvertStillPassthrough(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test png: %v", err)
	}

	out, report, err := Convert(buf.Bytes(), ModeRatio)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if !report.Passthrough {
		t.Error("expected passthrough for compliant still image")
	}

	if !bytes.Equal(out, buf.Bytes()) {
		t.Error("passthrough output differs from input")
	}
}

func TestConvertRejectsBadInput(t *testing.T) {
	_, _, err := Convert([]byte("this is not an image at all"), ModeRatio)
	if err == nil {
		t.Fatal("expected error for non-image input")
	}
}

func fullPaletted(w, h int, c color.RGBA) *image.Paletted {
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{c})
	for y := range h {
		for x := range w {
			img.SetColorIndex(x, y, 0)
		}
	}

	return img
}

func TestCompositeBackgroundDisposal(t *testing.T) {
	green := image.NewPaletted(image.Rect(10, 10, 20, 20), color.Palette{color.RGBA{G: 255, A: 255}})

	for y := 10; y < 20; y++ {
		for x := 10; x < 20; x++ {
			green.SetColorIndex(x, y, 0)
		}
	}

	g := &gif.GIF{
		Image: []*image.Paletted{
			fullPaletted(50, 50, color.RGBA{R: 255, A: 255}),
			green,
		},
		Delay:    []int{10, 10},
		Disposal: []byte{gif.DisposalBackground, gif.DisposalNone},
		Config:   image.Config{Width: 50, Height: 50},
	}

	frames := compositeFrames(g)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}

	if _, _, _, a := frames[1].NRGBAAt(5, 5).RGBA(); a != 0 {
		t.Error("background disposal did not clear the canvas")
	}

	if _, g_, _, _ := frames[1].NRGBAAt(15, 15).RGBA(); g_ == 0 {
		t.Error("subrect frame not composited")
	}
}

func TestCompositePreviousDisposal(t *testing.T) {
	green := image.NewPaletted(image.Rect(10, 10, 20, 20), color.Palette{color.RGBA{G: 255, A: 255}})

	for y := 10; y < 20; y++ {
		for x := 10; x < 20; x++ {
			green.SetColorIndex(x, y, 0)
		}
	}

	g := &gif.GIF{
		Image: []*image.Paletted{
			fullPaletted(50, 50, color.RGBA{R: 255, A: 255}),
			green,
			fullPaletted(50, 50, color.RGBA{B: 255, A: 255}),
		},
		Delay:    []int{10, 10, 10},
		Disposal: []byte{gif.DisposalPrevious, gif.DisposalNone, gif.DisposalNone},
		Config:   image.Config{Width: 50, Height: 50},
	}

	frames := compositeFrames(g)

	if _, _, _, a := frames[1].NRGBAAt(5, 5).RGBA(); a != 0 {
		t.Error("previous disposal did not restore the pre-frame canvas")
	}

	if _, g_, _, _ := frames[1].NRGBAAt(15, 15).RGBA(); g_ == 0 {
		t.Error("subrect frame not composited after restore")
	}
}
