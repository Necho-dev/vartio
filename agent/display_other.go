//go:build !windows && !darwin

package agent

import (
	"vartio/internal/entities"
)

func interactiveSession() error {
	return &entities.FrameError{Code: "not_supported", Message: "display is implemented on windows and darwin"}
}

func listScreens() []entities.Screen { return nil }

func captureFrame(entities.Screen, displayProfile) ([]byte, error) {
	return nil, interactiveSession()
}
