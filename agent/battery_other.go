//go:build !darwin && !windows && !linux

package agent

import "vartio/internal/entities"

func readBattery() *entities.Battery { return nil }
