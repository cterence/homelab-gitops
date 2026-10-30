package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

		pals, _, err := quantizeFrames(t.Context(), []*image.NRGBA{img}, 256)
		if err != nil {
			t.Fatalf("quantizing noise frame: %v", err)
		}

		g.Image = append(g.Image, pals[0])
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
			gotW, gotH := targetSize(tt.w, tt.h, tt.mode, 128)

			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("targetSize(%d, %d, %s, 128) = %dx%d, want %dx%d",
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
			out, report, err := Convert(t.Context(), tt.data, Options{Mode: tt.mode})
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

func TestConvertInterleavesSizeAndFrames(t *testing.T) {
	input := noiseGIF(t, 256, 256, 20, 3)

	out, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if len(out) > maxBytes {
		t.Errorf("output %d bytes, want <= %d", len(out), maxBytes)
	}

	if report.Colors < colorFloor {
		t.Errorf("colors = %d, want at least the %d-color floor", report.Colors, colorFloor)
	}

	// the interleaved ladder searches the frame count between half and
	// full at each size instead of jumping straight to one half
	if report.FramesOut <= report.FramesIn/2 || report.FramesOut > report.FramesIn {
		t.Errorf("frames = %d, want the searched maximum above %d", report.FramesOut, report.FramesIn/2)
	}

	if report.Width != 128 {
		t.Errorf("width = %d, want 128 (first size that fits after searching)", report.Width)
	}
}

func TestConvertDegradesSizeBeforeFrames(t *testing.T) {
	input := noiseGIF(t, 128, 128, 120, 2)

	out, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if len(out) > maxBytes {
		t.Errorf("output %d bytes, want <= %d", len(out), maxBytes)
	}

	if report.Colors < colorFloor {
		t.Errorf("colors = %d, want at least the %d-color floor", report.Colors, colorFloor)
	}

	if report.Width >= 128 && report.Height >= 128 && report.FramesOut == report.FramesIn {
		t.Error("nothing was degraded despite the budget being impossible")
	}

	// size degrades at every frame count before frames drop, so the
	// moment frames are lost, resolution is already at the bottom step
	if report.FramesOut < report.FramesIn && report.Width > 48 {
		t.Errorf("frames dropped at %dpx, want size degraded to 48px first", report.Width)
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

	pals, _, err := quantizeFrames(t.Context(), []*image.NRGBA{img}, 2)
	if err != nil {
		t.Fatal(err)
	}

	pal := pals[0]

	if got := len(pal.Palette); got > 3 {
		t.Errorf("palette has %d entries (2 colors + transparent), want <= 3", got)
	}

	if pal.Bounds() != img.Bounds() {
		t.Errorf("bounds %v, want %v", pal.Bounds(), img.Bounds())
	}
}

func TestQuantizeTransparent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(1, 0, color.NRGBA{A: 0})

	pals, _, err := quantizeFrames(t.Context(), []*image.NRGBA{img}, 4)
	if err != nil {
		t.Fatal(err)
	}

	pal := pals[0]

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

	out, report, err := Convert(t.Context(), buf.Bytes(), Options{Mode: ModeRatio})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.Width > maxSide || report.Height > maxSide {
		t.Errorf("output %dx%d exceeds %dpx", report.Width, report.Height, maxSide)
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

	out, report, err := Convert(t.Context(), buf.Bytes(), Options{Mode: ModeRatio})
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

func TestTargetSizeMaxSide(t *testing.T) {
	tests := []struct {
		name         string
		w, h         int
		mode         string
		side         int
		wantW, wantH int
	}{
		{"ratio smaller side", 200, 100, ModeRatio, 64, 64, 32},
		{"square smaller side", 200, 100, ModeSquare, 64, 64, 64},
		{"ratio already within side", 60, 40, ModeRatio, 64, 60, 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotW, gotH := targetSize(tt.w, tt.h, tt.mode, tt.side)

			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("targetSize(%d, %d, %s, %d) = %dx%d, want %dx%d",
					tt.w, tt.h, tt.mode, tt.side, gotW, gotH, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestSizeLadder(t *testing.T) {
	tests := []struct {
		maxSide int
		want    []int
	}{
		{128, []int{128, 96, 64, 48}},
		{96, []int{96, 64, 48}},
		{48, []int{48}},
		{32, []int{32}},
	}

	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.maxSide), func(t *testing.T) {
			got := sizeLadder(tt.maxSide)

			if !slices.Equal(got, tt.want) {
				t.Errorf("sizeLadder(%d) = %v, want %v", tt.maxSide, got, tt.want)
			}
		})
	}
}

func TestConvertFrameCap(t *testing.T) {
	input := noiseGIF(t, 256, 256, 20, 3)

	out, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio, PinFrames: 4})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.FramesOut != 4 {
		t.Errorf("frames = %d, want 4", report.FramesOut)
	}

	if len(out) > maxBytes {
		t.Errorf("output %d bytes, want <= %d", len(out), maxBytes)
	}

	if report.Colors < 64 {
		t.Errorf("colors = %d with 4 frames, want the freed budget spent on quality", report.Colors)
	}
}

func TestConvertMaxSide(t *testing.T) {
	input := noiseGIF(t, 256, 256, 20, 4)

	_, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio, PinSide: 64})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.Width > 64 || report.Height > 64 {
		t.Errorf("output %dx%d, want <= 64px", report.Width, report.Height)
	}

	if report.FramesOut != report.FramesIn {
		t.Errorf("frames = %d, want all kept at the smaller size", report.FramesOut)
	}
}

func TestConvertCapDisablesPassthrough(t *testing.T) {
	input := solidGIF(t, 64, 64, 3)

	_, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio, PinFrames: 1})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.Passthrough {
		t.Error("frame cap must force re-encoding, not passthrough")
	}

	if report.FramesOut != 1 {
		t.Errorf("frames = %d, want 1", report.FramesOut)
	}
}

func TestQuantizeDeltaFrames(t *testing.T) {
	frame := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	for y := range 4 {
		for x := range 4 {
			frame.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}

	// identical second frame: every pixel should delta to index 0
	pals, _, err := quantizeFrames(t.Context(), []*image.NRGBA{frame, frame}, 4)
	if err != nil {
		t.Fatal(err)
	}

	pal := pals

	if got := pal[1].ColorIndexAt(1, 1); got != 0 {
		t.Errorf("unchanged pixel stored as index %d, want transparent delta", got)
	}

	if got := pal[0].ColorIndexAt(1, 1); got == 0 {
		t.Error("first frame pixel marked as delta")
	}
}

func TestDeltaGIFCompositesIdentically(t *testing.T) {
	frame := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	for y := range 4 {
		for x := range 4 {
			frame.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}

	frames, _, err := quantizeFrames(t.Context(), []*image.NRGBA{frame, frame}, 4)
	if err != nil {
		t.Fatal(err)
	}

	out, err := encodeGIF(frames, nil, []int{10, 10}, 0)
	if err != nil {
		t.Fatalf("encodeGIF() error = %v", err)
	}

	g, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decoding delta gif: %v", err)
	}

	decoded, err := compositeFrames(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded) != 2 {
		t.Fatalf("got %d frames, want 2", len(decoded))
	}

	for i, f := range decoded {
		r, _, _, a := f.NRGBAAt(1, 1).RGBA()

		if a == 0 || r == 0 {
			t.Errorf("frame %d composites to a non-red pixel", i)
		}
	}
}

func TestConvertColorCap(t *testing.T) {
	input := noiseGIF(t, 256, 256, 20, 4)

	out, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio, PinSide: 64, PinColors: 16})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.Colors != 16 {
		t.Errorf("colors = %d, want the 16-color cap", report.Colors)
	}

	if len(out) > maxBytes {
		t.Errorf("output %d bytes, want <= %d", len(out), maxBytes)
	}
}

func TestConvertColorCapDisablesPassthrough(t *testing.T) {
	input := solidGIF(t, 64, 64, 3)

	_, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio, PinColors: 32})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if report.Passthrough {
		t.Error("color cap must force re-encoding, not passthrough")
	}
}

func TestConvertStillPassthroughReportContract(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(16, 16, color.NRGBA{G: 255, A: 255})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test png: %v", err)
	}

	_, report, err := Convert(t.Context(), buf.Bytes(), Options{Mode: ModeRatio})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	// the report must be fully populated on the passthrough path too
	if report.FramesOut != 1 || report.Colors != 2 {
		t.Errorf("passthrough report = %d frames, %d colors; want 1 and 2",
			report.FramesOut, report.Colors)
	}

	if report.Width != 32 || report.Height != 32 || report.OutputSize != len(buf.Bytes()) {
		t.Errorf("passthrough report dims = %dx%d, %d bytes; want 32x32, %d",
			report.Width, report.Height, report.OutputSize, len(buf.Bytes()))
	}
}

func tinyFrameGIF(t *testing.T, w, h, frames int) []byte {
	t.Helper()

	g := &gif.GIF{
		LoopCount: 0,
		Config:    image.Config{Width: w, Height: h},
	}

	one := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.RGBA{R: 255, A: 255}})

	for range frames {
		g.Image = append(g.Image, one)
		g.Delay = append(g.Delay, 10)
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encoding test gif: %v", err)
	}

	return buf.Bytes()
}

// a 49-byte GIF can claim a huge logical screen: the dims are rejected
// before decode allocates anything
func TestConvertRejectsScreenBomb(t *testing.T) {
	input := tinyFrameGIF(t, 65535, 65535, 1)

	_, _, err := Convert(t.Context(), input, Options{Mode: ModeRatio})
	if err == nil {
		t.Fatal("expected the oversized screen to be rejected")
	}

	if !strings.Contains(err.Error(), "per-side limit") {
		t.Errorf("error = %v, want the per-side limit", err)
	}
}

func TestConvertRejectsFrameBomb(t *testing.T) {
	input := tinyFrameGIF(t, 100, 100, maxInputFrames+1)

	_, _, err := Convert(t.Context(), input, Options{Mode: ModeRatio})
	if err == nil || !strings.Contains(err.Error(), "frame limit") {
		t.Errorf("error = %v, want the frame limit", err)
	}
}

func TestConvertRejectsTotalPixelBomb(t *testing.T) {
	// 2048x2040 fits the per-image budget; enough frames blow the total
	w, h := 2048, 2040
	frames := maxInputTotalPx/(w*h) + 1

	input := tinyFrameGIF(t, w, h, frames)

	_, _, err := Convert(t.Context(), input, Options{Mode: ModeRatio})
	if err == nil || !strings.Contains(err.Error(), "total-pixel limit") {
		t.Errorf("error = %v, want the total-pixel limit", err)
	}
}

func TestConvertRespectsDeadline(t *testing.T) {
	input := noiseGIF(t, 256, 256, 60, 9)

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Millisecond)
	defer cancel()

	time.Sleep(5 * time.Millisecond)

	_, _, err := Convert(ctx, input, Options{Mode: ModeRatio})
	if err == nil {
		t.Fatal("expected the canceled context to stop the conversion")
	}
}

func TestPinColorsDegradesFreeAxes(t *testing.T) {
	input := noiseGIF(t, 256, 256, 20, 3)

	out, report, err := Convert(t.Context(), input, Options{Mode: ModeRatio, PinColors: 200})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	// the pin is honored exactly; free axes paid for it
	if report.Colors != 200 {
		t.Errorf("colors = %d, want the pinned 200", report.Colors)
	}

	if report.Width >= 128 {
		t.Errorf("width = %d, want the free size degraded below 128", report.Width)
	}

	if len(out) > maxBytes {
		t.Errorf("output %d bytes, want <= %d", len(out), maxBytes)
	}
}

func TestPinnedValuesCannotFit(t *testing.T) {
	input := noiseGIF(t, 128, 128, 120, 2)

	_, _, err := Convert(t.Context(), input, Options{
		Mode:      ModeRatio,
		PinSide:   128,
		PinFrames: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "pinned values cannot fit") {
		t.Errorf("error = %v, want the cannot-fit error", err)
	}
}

func TestPriorityPinRelaxes(t *testing.T) {
	input := noiseGIF(t, 128, 128, 120, 2)

	_, report, err := Convert(t.Context(), input, Options{
		Mode:      ModeRatio,
		PinSide:   96,
		PinFrames: 100,
		Priority:  "frames",
	})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	// the pinned set was impossible, so the priority axis yielded and
	// the free search took over under the remaining pin
	if report.Width != 96 {
		t.Errorf("width = %d, want the pinned 96", report.Width)
	}

	if report.FramesOut >= report.FramesIn || report.Colors < colorFloor {
		t.Errorf("frames = %d, colors = %d, want degraded frames at the floor or above",
			report.FramesOut, report.Colors)
	}
}

func TestConvertRejectsBadInput(t *testing.T) {
	_, _, err := Convert(t.Context(), []byte("this is not an image at all"), Options{Mode: ModeRatio})
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

	frames, err := compositeFrames(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}

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

	frames, err := compositeFrames(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, _, a := frames[1].NRGBAAt(5, 5).RGBA(); a != 0 {
		t.Error("previous disposal did not restore the pre-frame canvas")
	}

	if _, g_, _, _ := frames[1].NRGBAAt(15, 15).RGBA(); g_ == 0 {
		t.Error("subrect frame not composited after restore")
	}
}

func TestConvertMovingSubjectOverTransparency(t *testing.T) {
	// a subject moving over a transparent background: every frame leaves
	// holes where it used to be, and a delta encoder that writes the
	// transparent index there ghosts the old subject instead of clearing
	const size, sub = 200, 20

	pal := color.Palette{color.RGBA{}, color.RGBA{R: 255, A: 255}}
	g := &gif.GIF{
		LoopCount: 0,
		Config:    image.Config{ColorModel: pal, Width: size, Height: size},
	}

	for i := range 4 {
		x0 := 20 + i*sub*2
		fr := image.NewPaletted(image.Rect(0, 0, size, size), pal)

		for y := 40; y < 40+sub; y++ {
			for x := x0; x < x0+sub; x++ {
				fr.SetColorIndex(x, y, 1)
			}
		}

		g.Image = append(g.Image, fr)
		g.Disposal = append(g.Disposal, gif.DisposalBackground)
		g.Delay = append(g.Delay, 10)
	}

	var in bytes.Buffer
	if err := gif.EncodeAll(&in, g); err != nil {
		t.Fatalf("encoding input: %v", err)
	}

	out, rep, err := Convert(t.Context(), in.Bytes(), Options{Mode: ModeSquare})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if rep.Passthrough {
		t.Fatal("expected conversion")
	}

	dg, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decoding output: %v", err)
	}

	frames, err := compositeFrames(t.Context(), dg)
	if err != nil {
		t.Fatalf("compositing output: %v", err)
	}

	scale := float64(rep.Width) / size

	// the subject core, 3 input pixels in from the edges: resize
	// interpolation blends the border with the transparent background
	subject := func(i, x, y int) bool {
		x0 := 20 + i*sub*2
		ix := int(float64(x) / scale)
		iy := int(float64(y) / scale)

		return ix >= x0+3 && ix < x0+sub-3 && iy >= 43 && iy < 57
	}

	for i, f := range frames {
		b := f.Bounds()

		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				opaque := f.NRGBAAt(x, y).A >= alphaThreshold

				if subject(i, x, y) && !opaque {
					t.Errorf("frame %d: subject pixel (%d,%d) is transparent", i, x, y)
				}

				// where the subject used to be, it must be gone
				for j := range i {
					if subject(j, x, y) && opaque {
						t.Errorf("frame %d: ghost of frame %d at (%d,%d)", i, j, x, y)
					}
				}
			}
		}
	}
}

func TestNormalizePins(t *testing.T) {
	tests := []struct {
		name   string
		opts   Options
		frames int
		w, h   int
		want   Options
	}{
		{
			name:   "square pin at the limit holds",
			opts:   Options{Mode: ModeSquare, PinSide: 128, Priority: "side"},
			frames: 10, w: 64, h: 64,
			want: Options{Mode: ModeSquare, PinSide: 128, Priority: "side"},
		},
		{
			name:   "ratio pin at the input side is a no-op",
			opts:   Options{Mode: ModeRatio, PinSide: 128, Priority: "side"},
			frames: 10, w: 64, h: 64,
			want: Options{Mode: ModeRatio},
		},
		{
			name:   "ratio pin below the input side holds",
			opts:   Options{Mode: ModeRatio, PinSide: 96, Priority: "side"},
			frames: 10, w: 500, h: 281,
			want: Options{Mode: ModeRatio, PinSide: 96, Priority: "side"},
		},
		{
			name:   "frames pin at the full count is a no-op",
			opts:   Options{Mode: ModeSquare, PinFrames: 10, Priority: "frames"},
			frames: 10, w: 64, h: 64,
			want: Options{Mode: ModeSquare},
		},
		{
			name:   "colors pin at the ceiling is a no-op",
			opts:   Options{Mode: ModeSquare, PinColors: 255, Priority: "colors"},
			frames: 10, w: 64, h: 64,
			want: Options{Mode: ModeSquare},
		},
		{
			name:   "unknown priority is dropped",
			opts:   Options{Mode: ModeSquare, Priority: "bogus"},
			frames: 10, w: 64, h: 64,
			want: Options{Mode: ModeSquare},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts
			normalizePins(&got, tt.frames, tt.w, tt.h)

			if got != tt.want {
				t.Errorf("normalizePins() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
