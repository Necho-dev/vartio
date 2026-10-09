//go:build windows

package main

import (
	"fmt"
	"image"
	"unsafe"

	"golang.org/x/sys/windows"

	"govmomi-wmks/internal/guest"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetSystemMetrics       = modUser32.NewProc("GetSystemMetrics")
	procGetDC                  = modUser32.NewProc("GetDC")
	procReleaseDC              = modUser32.NewProc("ReleaseDC")
	procCreateCompatibleDC     = modGdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = modGdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = modGdi32.NewProc("SelectObject")
	procBitBlt                 = modGdi32.NewProc("BitBlt")
	procDeleteObject           = modGdi32.NewProc("DeleteObject")
	procDeleteDC               = modGdi32.NewProc("DeleteDC")
	procGetDIBits              = modGdi32.NewProc("GetDIBits")
	procGetCurrentProcessId    = modKernel32.NewProc("GetCurrentProcessId")
	procProcessIdToSessionId   = modKernel32.NewProc("ProcessIdToSessionId")
)

const (
	smCXScreen   = 0
	smCYScreen   = 1
	srcCopy      = 0x00CC0020
	dibRGBColors = 0
	biRGB        = 0
	biBitCount   = 32
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func currentSessionID() uint32 {
	pid, _, _ := procGetCurrentProcessId.Call()
	var sid uint32
	r, _, _ := procProcessIdToSessionId.Call(pid, uintptr(unsafe.Pointer(&sid)))
	if r == 0 {
		return 0
	}
	return sid
}

func interactiveSession() error {
	if currentSessionID() == 0 {
		return &guest.FrameError{Code: "no_user_session", Message: "probe-agent is not in an interactive session (Session 0 cannot see the user desktop)"}
	}
	return nil
}

func captureFrame() ([]byte, error) {
	w, _, _ := procGetSystemMetrics.Call(smCXScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if w == 0 || h == 0 {
		return nil, &guest.FrameError{Code: "no_user_session", Message: "no desktop metrics"}
	}
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, hdc)

	mem, _, _ := procCreateCompatibleDC.Call(hdc)
	if mem == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(mem)

	bmp, _, _ := procCreateCompatibleBitmap.Call(hdc, w, h)
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)
	procSelectObject.Call(mem, bmp)
	r, _, _ := procBitBlt.Call(mem, 0, 0, w, h, hdc, 0, 0, srcCopy)
	if r == 0 {
		return nil, &guest.FrameError{Code: "no_user_session", Message: "BitBlt failed (no user desktop)"}
	}

	hdr := bitmapInfoHeader{
		Size:        40,
		Width:       int32(w),
		Height:      -int32(h),
		Planes:      1,
		BitCount:    biBitCount,
		Compression: biRGB,
	}
	pixels := make([]byte, int(w*h*4))
	ok, _, _ := procGetDIBits.Call(mem, bmp, 0, h, uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&hdr)), dibRGBColors)
	if ok == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for y := 0; y < int(h); y++ {
		row := y * int(w) * 4
		for x := 0; x < int(w); x++ {
			i := row + x*4
			// Windows BGRA → RGBA
			img.Pix[i+0] = pixels[i+2]
			img.Pix[i+1] = pixels[i+1]
			img.Pix[i+2] = pixels[i+0]
			img.Pix[i+3] = 0xff
		}
	}
	return encodeJPEG(img)
}
