//go:build !darwin && !windows && !linux

package agent

import "vartio/internal/entities"

func listApps() ([]entities.AppInfo, error) {
	return []entities.AppInfo{}, nil
}
