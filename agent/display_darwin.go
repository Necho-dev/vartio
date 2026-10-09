//go:build darwin

package main

import (
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"

	"govmomi-wmks/internal/guest"
)

func interactiveSession() error {
	return nil
}

func captureFrame() ([]byte, error) {
	f, err := os.CreateTemp("", "probe-display-*.jpg")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)

	cmd := exec.Command("screencapture", "-x", "-m", "-t", "jpg", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := string(out)
		if msg == "" {
			msg = err.Error()
		}
		return nil, &guest.FrameError{Code: "no_user_session", Message: "screen capture failed: " + msg}
	}
	rf, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	img, _, err := image.Decode(rf)
	if err != nil {
		return nil, err
	}
	return encodeJPEG(img)
}
