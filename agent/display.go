package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"runtime"
	"sync"
	"time"

	"govmomi-wmks/internal/guest"
)

// screenAll asks the capture loop to composite every monitor by its desktop
// position instead of streaming one of them.
const screenAll = -1

// displayProfile is one point on the frame-rate / picture-quality trade-off.
type displayProfile struct {
	FPS     int
	MaxSide int
	Quality int
}

var fixedProfiles = map[string]displayProfile{
	guest.DisplaySmooth: {FPS: 10, MaxSide: 1280, Quality: 60},
	guest.DisplayHD:     {FPS: 3, MaxSide: 2560, Quality: 90},
}

// autoLadder is what "auto" climbs and descends, cheapest first.
var autoLadder = []displayProfile{
	{FPS: 2, MaxSide: 1280, Quality: 55},
	{FPS: 4, MaxSide: 1440, Quality: 65},
	{FPS: 6, MaxSide: 1600, Quality: 72},
	{FPS: 8, MaxSide: 1920, Quality: 78},
	{FPS: 10, MaxSide: 1920, Quality: 82},
}

const (
	autoStart  = 2
	autoWindow = 2 * time.Second
	// autoUpAfter healthy windows in a row are needed before stepping up, so a
	// link that only just copes does not oscillate.
	autoUpAfter = 3
)

func normQuality(q string) string {
	if _, ok := fixedProfiles[q]; ok {
		return q
	}
	return guest.DisplayAuto
}

// displayHub keeps one capture per browser page. Screen and quality belong to
// that page: opening or switching in a second page must not retarget the first.
type displayHub struct {
	mu    sync.Mutex
	views map[string]*displayView
}

// displayView is one viewer's capture loop.
type displayView struct {
	mu      sync.Mutex
	stop    context.CancelFunc
	screens []guest.Screen
	cur     int
	all     bool
	// gen bumps whenever this viewer's choice changes, so a frame captured
	// under the old choice is dropped instead of being sent after they switch.
	gen     int
	quality string
	level   int
	// auto-mode measurement window
	winStart        time.Time
	winN            int
	winProd, winOut time.Duration
	good            int
}

func newDisplayHub() *displayHub { return &displayHub{views: map[string]*displayView{}} }

func newDisplayView(q string) *displayView {
	return &displayView{quality: normQuality(q), level: autoStart}
}

func (d *displayHub) open(id string, req guest.DisplayOpenRequest, send func([]byte)) (guest.DisplayOpenResult, *guest.FrameError) {
	if id == "" {
		return guest.DisplayOpenResult{}, &guest.FrameError{Code: "bad_request", Message: "display id is required"}
	}
	if err := interactiveSession(); err != nil {
		return guest.DisplayOpenResult{}, asRPC(err)
	}
	screens := listScreens()
	sc := pickScreen(screens, req.Screen)
	q := normQuality(req.Quality)
	v := newDisplayView(q)
	first, err := captureFrame(sc, v.profileLocked())
	if err != nil {
		return guest.DisplayOpenResult{}, asRPC(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.mu.Lock()
	v.stop = cancel
	v.screens = screens
	v.cur = sc.Index
	v.all = false
	v.gen++
	v.mu.Unlock()

	d.mu.Lock()
	if old := d.views[id]; old != nil {
		delete(d.views, id)
		d.mu.Unlock()
		old.close()
		d.mu.Lock()
	}
	if len(d.views) >= guest.MaxDisplays {
		d.mu.Unlock()
		cancel()
		return guest.DisplayOpenResult{}, &guest.FrameError{Code: "too_many", Message: fmt.Sprintf("同一台机器最多 %d 路实时画面", guest.MaxDisplays)}
	}
	d.views[id] = v
	d.mu.Unlock()
	send(first)
	go v.loop(ctx, send)
	return guest.DisplayOpenResult{OK: true, FPS: autoLadder[len(autoLadder)-1].FPS, MaxSide: fixedProfiles[guest.DisplayHD].MaxSide,
		Screens: screens, Screen: sc.Index, Quality: q}, nil
}

// view finds the named capture. An empty id addresses the only open one, so a
// caller that predates per-viewer ids still works until a second page opens.
func (d *displayHub) view(id string) *displayView {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id != "" {
		return d.views[id]
	}
	if len(d.views) == 1 {
		for _, v := range d.views {
			return v
		}
	}
	return nil
}

// pickScreen falls back to the primary monitor (index 0) for an unknown index.
// With no list at all it returns the zero Screen, which captures the primary.
func pickScreen(screens []guest.Screen, i int) guest.Screen {
	if i >= 0 && i < len(screens) {
		return screens[i]
	}
	if len(screens) > 0 {
		return screens[0]
	}
	return guest.Screen{}
}

func (d *displayHub) selectScreen(id string, i int) *guest.FrameError {
	v := d.view(id)
	if v == nil {
		return &guest.FrameError{Code: "not_open", Message: "display is not open"}
	}
	return v.selectScreen(i)
}

func (v *displayView) selectScreen(i int) *guest.FrameError {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stop == nil {
		return &guest.FrameError{Code: "not_open", Message: "display is not open"}
	}
	if i == screenAll {
		if len(v.screens) < 2 {
			return &guest.FrameError{Code: "bad_request", Message: "no such screen"}
		}
		v.all = true
		v.gen++
		return nil
	}
	if i < 0 || i >= len(v.screens) {
		return &guest.FrameError{Code: "bad_request", Message: "no such screen"}
	}
	v.cur = i
	v.all = false
	v.gen++
	return nil
}

func (d *displayHub) setQuality(id, q string) string {
	v := d.view(id)
	if v == nil {
		return normQuality(q)
	}
	return v.setQuality(q)
}

func (v *displayView) setQuality(q string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.quality = normQuality(q)
	v.good, v.winN = 0, 0
	return v.quality
}

func (v *displayView) profileLocked() displayProfile {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.profile()
}

func (v *displayView) profile() displayProfile {
	if p, ok := fixedProfiles[v.quality]; ok {
		return p
	}
	return autoLadder[v.level]
}

func (v *displayView) current() (guest.Screen, displayProfile) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return pickScreen(v.screens, v.cur), v.profile()
}

// plan is what the next frame should be. all is the arranged multi-monitor view.
func (v *displayView) plan() (all bool, screens []guest.Screen, sc guest.Screen, p displayProfile, gen int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	screens = append([]guest.Screen(nil), v.screens...)
	return v.all && len(screens) > 1, screens, pickScreen(screens, v.cur), v.profile(), v.gen
}

// observe feeds one frame's timings to the auto controller: prod is capture +
// encode, out is the time the frame took to leave on the hub connection.
func (v *displayView) observe(prod, out time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.quality != guest.DisplayAuto {
		return
	}
	now := time.Now()
	if v.winN == 0 {
		v.winStart, v.winProd, v.winOut = now, 0, 0
	}
	v.winN++
	v.winProd += prod
	v.winOut += out
	if now.Sub(v.winStart) < autoWindow {
		return
	}
	n := time.Duration(v.winN)
	v.level, v.good = autoStep(v.level, v.good, v.winProd/n, v.winOut/n)
	v.winN = 0
}

// autoStep moves one rung down when sending saturates the link or the machine
// cannot produce frames in time, and one rung up after autoUpAfter windows
// with plenty of headroom.
func autoStep(level, good int, prod, out time.Duration) (int, int) {
	iv := time.Second / time.Duration(autoLadder[level].FPS)
	switch {
	case out > iv/2 || prod+out > iv*9/10:
		if level > 0 {
			level--
		}
		return level, 0
	case out < iv/5 && prod+out < iv*6/10:
		good++
		if good >= autoUpAfter && level < len(autoLadder)-1 {
			return level + 1, 0
		}
		return level, good
	default:
		return level, 0
	}
}

func (v *displayView) loop(ctx context.Context, send func([]byte)) {
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		all, screens, sc, p, gen := v.plan()
		var b []byte
		var err error
		if all {
			b, err = captureMosaic(screens, p)
		} else {
			b, err = captureFrame(sc, p)
		}
		if err != nil || ctx.Err() != nil {
			return
		}
		// This viewer switched (or closed) while the frame was being captured.
		if _, _, _, _, gen2 := v.plan(); gen2 != gen {
			continue
		}
		sent := time.Now()
		send(b)
		v.observe(sent.Sub(start), time.Since(sent))
		wait := max(time.Second/time.Duration(p.FPS)-time.Since(start), 10*time.Millisecond)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// close stops one viewer. An empty id stops every viewer, which is what a
// dropped agent connection does.
func (d *displayHub) close(id string) {
	d.mu.Lock()
	var drop []*displayView
	if id == "" {
		for _, v := range d.views {
			drop = append(drop, v)
		}
		d.views = map[string]*displayView{}
	} else if v := d.views[id]; v != nil {
		drop = []*displayView{v}
		delete(d.views, id)
	}
	d.mu.Unlock()
	for _, v := range drop {
		v.close()
	}
}

func (v *displayView) close() {
	v.mu.Lock()
	c := v.stop
	v.stop = nil
	v.all = false
	v.gen++
	v.mu.Unlock()
	if c != nil {
		c()
	}
}

// captureMosaic grabs every monitor and places it on one canvas using the
// desktop origin each screen reported, then encodes that overview.
func captureMosaic(screens []guest.Screen, p displayProfile) ([]byte, error) {
	imgs := make([]image.Image, len(screens))
	var last error
	ok := 0
	for i, s := range screens {
		if s.Width <= 0 || s.Height <= 0 {
			continue
		}
		tile := p
		tile.MaxSide = mosaicSlot(screens, i, p.MaxSide)
		raw, err := captureFrame(s, tile)
		if err != nil {
			last = err
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			last = err
			continue
		}
		imgs[i] = img
		ok++
	}
	if ok == 0 {
		if last == nil {
			last = &guest.FrameError{Code: "no_user_session", Message: "no screen to arrange"}
		}
		return nil, last
	}
	return encodeJPEG(composeMosaic(screens, imgs, p.MaxSide), p)
}

// mosaicSlot is the longer side of one screen after the whole desktop is fit
// to maxSide, so each capture is already close to the pixels it will occupy.
func mosaicSlot(screens []guest.Screen, i, maxSide int) int {
	_, _, scale := mosaicScale(screens, maxSide)
	s := screens[i]
	w := int(math.Round(float64(s.Width) * scale))
	h := int(math.Round(float64(s.Height) * scale))
	if w < h {
		w = h
	}
	if w < 1 {
		return 1
	}
	return w
}

func mosaicScale(screens []guest.Screen, maxSide int) (minX, minY int, scale float64) {
	minX, minY, maxX, maxY, ok := deskBounds(screens)
	if !ok {
		return 0, 0, 1
	}
	dw, dh := maxX-minX, maxY-minY
	side := dw
	if dh > side {
		side = dh
	}
	scale = 1
	if maxSide > 0 && side > maxSide {
		scale = float64(maxSide) / float64(side)
	}
	return minX, minY, scale
}

func deskBounds(screens []guest.Screen) (minX, minY, maxX, maxY int, ok bool) {
	for _, s := range screens {
		if s.Width <= 0 || s.Height <= 0 {
			continue
		}
		x2, y2 := s.X+s.Width, s.Y+s.Height
		if !ok {
			minX, minY, maxX, maxY, ok = s.X, s.Y, x2, y2, true
			continue
		}
		if s.X < minX {
			minX = s.X
		}
		if s.Y < minY {
			minY = s.Y
		}
		if x2 > maxX {
			maxX = x2
		}
		if y2 > maxY {
			maxY = y2
		}
	}
	return minX, minY, maxX, maxY, ok
}

// composeMosaic paints imgs onto a black desktop. A nil image leaves its
// rectangle black. The result's longer side is at most maxSide.
func composeMosaic(screens []guest.Screen, imgs []image.Image, maxSide int) *image.RGBA {
	minX, minY, maxX, maxY, ok := deskBounds(screens)
	if !ok {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	_, _, scale := mosaicScale(screens, maxSide)
	cw := max(int(math.Round(float64(maxX-minX)*scale)), 1)
	ch := max(int(math.Round(float64(maxY-minY)*scale)), 1)
	dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	for i, s := range screens {
		if i >= len(imgs) || imgs[i] == nil || s.Width <= 0 || s.Height <= 0 {
			continue
		}
		x := int(math.Round(float64(s.X-minX) * scale))
		y := int(math.Round(float64(s.Y-minY) * scale))
		w := max(int(math.Round(float64(s.Width)*scale)), 1)
		h := max(int(math.Round(float64(s.Height)*scale)), 1)
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x+w > cw {
			w = cw - x
		}
		if y+h > ch {
			h = ch - y
		}
		if w < 1 || h < 1 {
			continue
		}
		src := image.NewRGBA(image.Rect(0, 0, imgs[i].Bounds().Dx(), imgs[i].Bounds().Dy()))
		draw.Draw(src, src.Bounds(), imgs[i], imgs[i].Bounds().Min, draw.Src)
		fitted := src
		if src.Bounds().Dx() != w || src.Bounds().Dy() != h {
			fitted = resizeBilinear(src, w, h)
		}
		draw.Draw(dst, image.Rect(x, y, x+w, y+h), fitted, image.Point{}, draw.Src)
	}
	return dst
}

func encodeJPEG(img image.Image, p displayProfile) ([]byte, error) {
	scaled := scaleMaxSide(img, p.MaxSide)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: p.Quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func scaleMaxSide(src image.Image, maxSide int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || maxSide <= 0 || (w <= maxSide && h <= maxSide) {
		return src
	}
	nw, nh := maxSide, h*maxSide/w
	if h > w {
		nw, nh = w*maxSide/h, maxSide
	}
	rgba, ok := src.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	}
	return resizeBilinear(rgba, max(nw, 1), max(nh, 1))
}

// resizeBilinear works in 8-bit fixed point and splits rows across CPUs; it
// is only used for downscaling, where bilinear is good enough for a preview.
func resizeBilinear(src *image.RGBA, nw, nh int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	x0s, x1s, wxs := make([]int, nw), make([]int, nw), make([]uint32, nw)
	for x := range nw {
		x0, x1, f := srcPos(x, nw, w)
		x0s[x], x1s[x], wxs[x] = x0*4, x1*4, f
	}
	rows := func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sy0, sy1, wy := srcPos(y, nh, h)
			r0 := src.Pix[sy0*src.Stride:]
			r1 := src.Pix[sy1*src.Stride:]
			out := dst.Pix[y*dst.Stride:]
			for x := range nw {
				a, c, wx := x0s[x], x1s[x], wxs[x]
				for k := range 3 {
					top := uint32(r0[a+k])*(256-wx) + uint32(r0[c+k])*wx
					bot := uint32(r1[a+k])*(256-wx) + uint32(r1[c+k])*wx
					out[x*4+k] = uint8((top*(256-wy) + bot*wy + 1<<15) >> 16)
				}
				out[x*4+3] = 0xff
			}
		}
	}
	workers := min(runtime.NumCPU(), 8, nh)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rows(nh*i/workers, nh*(i+1)/workers)
		}(i)
	}
	wg.Wait()
	return dst
}

// srcPos maps destination pixel i (of n) onto a source axis of length size:
// the two neighbouring source pixels and the weight of the second, out of 256.
func srcPos(i, n, size int) (int, int, uint32) {
	f := (float64(i)+0.5)*float64(size)/float64(n) - 0.5
	if f < 0 {
		f = 0
	}
	i0 := int(f)
	i1 := min(i0+1, size-1)
	return i0, i1, uint32((f - float64(i0)) * 256)
}
