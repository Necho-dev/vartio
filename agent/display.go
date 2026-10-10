package main

import (
	"bytes"
	"context"
	"image"
	"image/draw"
	"image/jpeg"
	"runtime"
	"sync"
	"time"

	"govmomi-wmks/internal/guest"
)

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

type displayHub struct {
	mu      sync.Mutex
	stop    context.CancelFunc
	screens []guest.Screen
	cur     int
	quality string
	level   int
	// auto-mode measurement window
	winStart        time.Time
	winN            int
	winProd, winOut time.Duration
	good            int
}

func newDisplayHub() *displayHub { return &displayHub{quality: guest.DisplayAuto, level: autoStart} }

func (d *displayHub) open(req guest.DisplayOpenRequest, send func([]byte)) (guest.DisplayOpenResult, *guest.FrameError) {
	if err := interactiveSession(); err != nil {
		return guest.DisplayOpenResult{}, asRPC(err)
	}
	screens := listScreens()
	sc := pickScreen(screens, req.Screen)
	q := normQuality(req.Quality)
	d.close()
	d.mu.Lock()
	d.quality, d.level, d.good, d.winN = q, autoStart, 0, 0
	p := d.profileLocked()
	d.mu.Unlock()
	first, err := captureFrame(sc, p)
	if err != nil {
		return guest.DisplayOpenResult{}, asRPC(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.stop = cancel
	d.screens = screens
	d.cur = sc.Index
	d.mu.Unlock()
	send(first)
	go d.loop(ctx, send)
	return guest.DisplayOpenResult{OK: true, FPS: autoLadder[len(autoLadder)-1].FPS, MaxSide: fixedProfiles[guest.DisplayHD].MaxSide,
		Screens: screens, Screen: sc.Index, Quality: q}, nil
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

func (d *displayHub) selectScreen(i int) *guest.FrameError {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop == nil {
		return &guest.FrameError{Code: "not_open", Message: "display is not open"}
	}
	if i < 0 || i >= len(d.screens) {
		return &guest.FrameError{Code: "bad_request", Message: "no such screen"}
	}
	d.cur = i
	return nil
}

func (d *displayHub) setQuality(q string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.quality = normQuality(q)
	d.good, d.winN = 0, 0
	return d.quality
}

func (d *displayHub) profileLocked() displayProfile {
	if p, ok := fixedProfiles[d.quality]; ok {
		return p
	}
	return autoLadder[d.level]
}

func (d *displayHub) current() (guest.Screen, displayProfile) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return pickScreen(d.screens, d.cur), d.profileLocked()
}

// observe feeds one frame's timings to the auto controller: prod is capture +
// encode, out is the time the frame took to leave on the hub connection.
func (d *displayHub) observe(prod, out time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.quality != guest.DisplayAuto {
		return
	}
	now := time.Now()
	if d.winN == 0 {
		d.winStart, d.winProd, d.winOut = now, 0, 0
	}
	d.winN++
	d.winProd += prod
	d.winOut += out
	if now.Sub(d.winStart) < autoWindow {
		return
	}
	n := time.Duration(d.winN)
	d.level, d.good = autoStep(d.level, d.good, d.winProd/n, d.winOut/n)
	d.winN = 0
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

func (d *displayHub) loop(ctx context.Context, send func([]byte)) {
	for {
		start := time.Now()
		sc, p := d.current()
		b, err := captureFrame(sc, p)
		if err != nil {
			return
		}
		sent := time.Now()
		send(b)
		d.observe(sent.Sub(start), time.Since(sent))
		wait := time.Second/time.Duration(p.FPS) - time.Since(start)
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (d *displayHub) close() {
	d.mu.Lock()
	c := d.stop
	d.stop = nil
	d.mu.Unlock()
	if c != nil {
		c()
	}
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
	for x := 0; x < nw; x++ {
		x0, x1, f := srcPos(x, nw, w)
		x0s[x], x1s[x], wxs[x] = x0*4, x1*4, f
	}
	rows := func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sy0, sy1, wy := srcPos(y, nh, h)
			r0 := src.Pix[sy0*src.Stride:]
			r1 := src.Pix[sy1*src.Stride:]
			out := dst.Pix[y*dst.Stride:]
			for x := 0; x < nw; x++ {
				a, c, wx := x0s[x], x1s[x], wxs[x]
				for k := 0; k < 3; k++ {
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
	for i := 0; i < workers; i++ {
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
