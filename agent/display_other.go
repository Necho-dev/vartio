//go:build !windows && !darwin

package main

import "govmomi-wmks/internal/guest"

func interactiveSession() error {
	return &guest.FrameError{Code: "not_supported", Message: "display is implemented on windows and darwin"}
}

func listScreens() []guest.Screen { return nil }

func captureFrame(guest.Screen, displayProfile) ([]byte, error) {
	return nil, interactiveSession()
}
