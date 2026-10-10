package main

import (
	"image"
	"image/color"
	"testing"
	"time"

	"govmomi-wmks/internal/guest"
)

func TestDisplaySelectScreen(t *testing.T) {
	screens := []guest.Screen{{Index: 0, Primary: true}, {Index: 1, X: 1920}}
	if got := pickScreen(screens, 5); got.Index != 0 {
		t.Fatalf("unknown index should fall back to primary, got %d", got.Index)
	}
	if got := pickScreen(nil, 1); got.Width != 0 {
		t.Fatalf("empty list should give the zero screen, got %+v", got)
	}
	d := newDisplayHub()
	if err := d.selectScreen(1); err == nil {
		t.Fatal("select before open should fail")
	}
	d.stop = func() {}
	d.screens = screens
	if err := d.selectScreen(2); err == nil {
		t.Fatal("out-of-range select should fail")
	}
	if err := d.selectScreen(1); err != nil {
		t.Fatal(err)
	}
	if sc, _ := d.current(); sc.X != 1920 {
		t.Fatalf("select 1 gave %+v", sc)
	}
}

func TestDisplayQualityProfiles(t *testing.T) {
	d := newDisplayHub()
	if _, p := d.current(); p != autoLadder[autoStart] {
		t.Fatalf("default should be auto at the start rung, got %+v", p)
	}
	if q := d.setQuality("hd"); q != guest.DisplayHD {
		t.Fatal(q)
	}
	if _, p := d.current(); p.Quality <= autoLadder[len(autoLadder)-1].Quality || p.FPS >= autoLadder[autoStart].FPS {
		t.Fatalf("hd should trade frame rate for quality: %+v", p)
	}
	if _, p := d.current(); d.setQuality("smooth") != guest.DisplaySmooth || p.FPS != 3 {
		t.Fatal("switch to smooth")
	}
	if _, p := d.current(); p.FPS != 10 {
		t.Fatalf("smooth should run at 10 fps: %+v", p)
	}
	if d.setQuality("bogus") != guest.DisplayAuto {
		t.Fatal("unknown profile should mean auto")
	}
}

func TestAutoStep(t *testing.T) {
	iv := time.Second / time.Duration(autoLadder[autoStart].FPS)
	if l, _ := autoStep(autoStart, 2, iv/10, iv*6/10); l != autoStart-1 {
		t.Fatalf("slow send should step down, got %d", l)
	}
	if l, _ := autoStep(autoStart, 0, iv, 0); l != autoStart-1 {
		t.Fatalf("slow capture should step down, got %d", l)
	}
	l, g := autoStart, 0
	for i := 0; i < autoUpAfter-1; i++ {
		if l, g = autoStep(l, g, iv/10, iv/20); l != autoStart {
			t.Fatalf("stepped up too early after %d windows", i+1)
		}
	}
	if l, _ = autoStep(l, g, iv/10, iv/20); l != autoStart+1 {
		t.Fatalf("headroom should step up, got %d", l)
	}
	if l, _ := autoStep(0, 0, time.Second, time.Second); l != 0 {
		t.Fatal("bottom rung must hold")
	}
}

func TestScaleMaxSide(t *testing.T) {
	src := image.NewYCbCr(image.Rect(0, 0, 400, 200), image.YCbCrSubsampleRatio420)
	for i := range src.Y {
		src.Y[i] = 200
	}
	for i := range src.Cb {
		src.Cb[i], src.Cr[i] = 128, 128
	}
	out := scaleMaxSide(src, 100)
	if b := out.Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Fatalf("size %v", b)
	}
	c := color.RGBAModel.Convert(out.At(50, 25)).(color.RGBA)
	if c.R < 195 || c.R > 205 || c.R != c.G || c.G != c.B {
		t.Fatalf("flat grey should stay grey, got %+v", c)
	}
	if scaleMaxSide(src, 1000) != image.Image(src) {
		t.Fatal("no upscaling")
	}
}
