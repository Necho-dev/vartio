//go:build !windows

package agent

import (
	"vartio/internal/entities"
)

func listServices() ([]entities.ServiceInfo, error) {
	return []entities.ServiceInfo{}, nil
}

func serviceAlive(_ string) (bool, string) {
	return false, "services are windows-only"
}

func serviceAction(_ entities.ActionRequest) (entities.ActionResult, *entities.FrameError) {
	return entities.ActionResult{}, &entities.FrameError{Code: "not_supported", Message: "windows services only"}
}
