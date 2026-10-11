package main

import (
	"image"
	"image/color"
	"image/draw"
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
	v := newDisplayView(guest.DisplayAuto)
	if err := v.selectScreen(1); err == nil {
		t.Fatal("select before open should fail")
	}
	v.stop = func() {}
	v.screens = screens
	if err := v.selectScreen(2); err == nil {
		t.Fatal("out-of-range select should fail")
	}
	if err := v.selectScreen(1); err != nil {
		t.Fatal(err)
	}
	if sc, _ := v.current(); sc.X != 1920 {
		t.Fatalf("select 1 gave %+v", sc)
	}
	if err := v.selectScreen(-1); err != nil {
		t.Fatal(err)
	}
	if all, _, sc, _, _ := v.plan(); !all || sc.X != 1920 {
		t.Fatalf("arranged view should keep the last screen, all=%v %+v", all, sc)
	}
	if err := v.selectScreen(-2); err == nil {
		t.Fatal("unknown index")
	}
	if err := v.selectScreen(0); err != nil {
		t.Fatal(err)
	}
	if all, _, _, _, _ := v.plan(); all {
		t.Fatal("picking a screen should leave the arranged view")
	}
}

func TestDisplayCloseClearsArranged(t *testing.T) {
	v := newDisplayView(guest.DisplayAuto)
	v.stop = func() {}
	v.screens = []guest.Screen{{Index: 0}, {Index: 1}}
	if err := v.selectScreen(screenAll); err != nil {
		t.Fatal(err)
	}
	v.close()
	if all, _, _, _, _ := v.plan(); all {
		t.Fatal("closing the stream should stop arranging every screen")
	}
	// The next open starts on one monitor. Re-arming the view the way open does
	// must not bring the arranged view back.
	v.stop = func() {}
	v.mu.Lock()
	v.screens = []guest.Screen{{Index: 0}, {Index: 1}}
	v.cur = 0
	v.all = false
	v.mu.Unlock()
	if all, _, sc, _, _ := v.plan(); all || sc.Index != 0 {
		t.Fatalf("a new stream should start on one screen, all=%v index=%d", all, sc.Index)
	}
}

func TestDisplayViewsAreIndependent(t *testing.T) {
	screens := []guest.Screen{{Index: 0, Primary: true}, {Index: 1, X: 1920}}
	arm := func() *displayView {
		v := newDisplayView(guest.DisplayAuto)
		v.stop = func() {}
		v.screens = screens
		return v
	}
	d := newDisplayHub()
	a, b := arm(), arm()
	d.views["a"], d.views["b"] = a, b
	if err := d.selectScreen("b", 1); err != nil {
		t.Fatal(err)
	}
	if sc, _ := a.current(); sc.Index != 0 {
		t.Fatalf("viewer A moved to screen %d", sc.Index)
	}
	if sc, _ := b.current(); sc.Index != 1 {
		t.Fatalf("viewer B stayed on screen %d", sc.Index)
	}
	if err := d.selectScreen("a", screenAll); err != nil {
		t.Fatal(err)
	}
	if all, _, _, _, _ := b.plan(); all {
		t.Fatal("arranging A also arranged B")
	}
	if d.setQuality("a", guest.DisplayHD) != guest.DisplayHD {
		t.Fatal("quality")
	}
	if _, p := b.current(); p.FPS != autoLadder[autoStart].FPS {
		t.Fatalf("B quality changed: %+v", p)
	}
	d.close("a")
	if _, ok := d.views["a"]; ok {
		t.Fatal("A still registered")
	}
	if sc, _ := b.current(); sc.Index != 1 || b.stop == nil {
		t.Fatal("closing A stopped B")
	}
	d.close("")
	if len(d.views) != 0 || b.stop != nil {
		t.Fatal("close all should stop the remaining viewer")
	}
}

func TestComposeMosaic(t *testing.T) {
	screens := []guest.Screen{
		{X: 0, Y: 0, Width: 100, Height: 50},
		{X: 100, Y: -80, Width: 40, Height: 80},
	}
	red := solid(10, 5, color.RGBA{R: 255, A: 255})
	blue := solid(4, 8, color.RGBA{B: 255, A: 255})
	out := composeMosaic(screens, []image.Image{red, blue}, 70)
	if b := out.Bounds(); b.Dx() != 70 || b.Dy() != 65 {
		t.Fatalf("canvas %v, want 70x65", b)
	}
	if c := out.RGBAAt(10, 50); c.R < 200 || c.B > 20 {
		t.Fatalf("primary at (10,50) = %+v", c)
	}
	if c := out.RGBAAt(55, 10); c.B < 200 || c.R > 20 {
		t.Fatalf("second at (55,10) = %+v", c)
	}
	if c := out.RGBAAt(10, 5); c.R > 10 || c.G > 10 || c.B > 10 {
		t.Fatalf("gap at (10,5) = %+v", c)
	}
}

func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

func TestDisplayQualityProfiles(t *testing.T) {
	v := newDisplayView(guest.DisplayAuto)
	if _, p := v.current(); p != autoLadder[autoStart] {
		t.Fatalf("default should be auto at the start rung, got %+v", p)
	}
	if q := v.setQuality("hd"); q != guest.DisplayHD {
		t.Fatal(q)
	}
	if _, p := v.current(); p.Quality <= autoLadder[len(autoLadder)-1].Quality || p.FPS >= autoLadder[autoStart].FPS {
		t.Fatalf("hd should trade frame rate for quality: %+v", p)
	}
	if _, p := v.current(); v.setQuality("smooth") != guest.DisplaySmooth || p.FPS != 3 {
		t.Fatal("switch to smooth")
	}
	if _, p := v.current(); p.FPS != 10 {
		t.Fatalf("smooth should run at 10 fps: %+v", p)
	}
	if v.setQuality("bogus") != guest.DisplayAuto {
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
	for i := range autoUpAfter - 1 {
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
