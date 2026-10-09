package main

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"sync"
	"time"

	"govmomi-wmks/internal/guest"
)

type displayHub struct {
	mu   sync.Mutex
	stop context.CancelFunc
}

func newDisplayHub() *displayHub { return &displayHub{} }

func (d *displayHub) open(send func([]byte)) *guest.FrameError {
	if err := interactiveSession(); err != nil {
		return asRPC(err)
	}
	first, err := captureFrame()
	if err != nil {
		return asRPC(err)
	}
	d.close()
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.stop = cancel
	d.mu.Unlock()
	send(first)
	go d.loop(ctx, send)
	return nil
}

func (d *displayHub) loop(ctx context.Context, send func([]byte)) {
	t := time.NewTicker(time.Second / time.Duration(guest.DisplayFPS))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b, err := captureFrame()
			if err != nil {
				return
			}
			send(b)
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

func encodeJPEG(img image.Image) ([]byte, error) {
	scaled := scaleMaxSide(img, guest.DisplayMaxSide)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: guest.DisplayJPEGQ}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func scaleMaxSide(src image.Image, maxSide int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || (w <= maxSide && h <= maxSide) {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = maxSide
		nh = h * maxSide / w
	} else {
		nh = maxSide
		nw = w * maxSide / h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	rgba, ok := src.(*image.RGBA)
	if !ok {
		tmp := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				tmp.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		rgba = tmp
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		fy := (float64(y)+0.5)*float64(h)/float64(nh) - 0.5
		y0 := int(fy)
		if y0 < 0 {
			y0 = 0
		}
		y1 := y0 + 1
		if y1 >= h {
			y1 = h - 1
		}
		ty := fy - float64(y0)
		if ty < 0 {
			ty = 0
		}
		for x := 0; x < nw; x++ {
			fx := (float64(x)+0.5)*float64(w)/float64(nw) - 0.5
			x0 := int(fx)
			if x0 < 0 {
				x0 = 0
			}
			x1 := x0 + 1
			if x1 >= w {
				x1 = w - 1
			}
			tx := fx - float64(x0)
			if tx < 0 {
				tx = 0
			}
			c00 := sampleRGBA(rgba, x0, y0)
			c10 := sampleRGBA(rgba, x1, y0)
			c01 := sampleRGBA(rgba, x0, y1)
			c11 := sampleRGBA(rgba, x1, y1)
			off := y*dst.Stride + x*4
			dst.Pix[off+0] = lerp2(c00[0], c10[0], c01[0], c11[0], tx, ty)
			dst.Pix[off+1] = lerp2(c00[1], c10[1], c01[1], c11[1], tx, ty)
			dst.Pix[off+2] = lerp2(c00[2], c10[2], c01[2], c11[2], tx, ty)
			dst.Pix[off+3] = 0xff
		}
	}
	return dst
}

func sampleRGBA(img *image.RGBA, x, y int) [3]float64 {
	i := img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y)
	return [3]float64{float64(img.Pix[i]), float64(img.Pix[i+1]), float64(img.Pix[i+2])}
}

func lerp2(c00, c10, c01, c11, tx, ty float64) uint8 {
	a := c00 + (c10-c00)*tx
	b := c01 + (c11-c01)*tx
	v := a + (b-a)*ty
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}
