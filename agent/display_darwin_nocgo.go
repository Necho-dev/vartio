//go:build darwin && !cgo

package agent

import "vartio/internal/entities"

func interactiveSession() error {
	return &entities.FrameError{Code: "not_supported", Message: "display needs the cgo build (menu bar / screen capture)"}
}

func listScreens() []entities.Screen { return nil }

func captureFrame(entities.Screen, displayProfile) ([]byte, error) {
	return nil, interactiveSession()
}
