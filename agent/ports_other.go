//go:build !darwin && !windows && !linux

package agent

import "vartio/internal/entities"

func listPorts() ([]entities.PortInfo, error) {
	return []entities.PortInfo{}, nil
}
