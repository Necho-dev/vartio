//go:build !windows

package main

import "govmomi-wmks/internal/guest"

func listServices() ([]guest.ServiceInfo, error) {
	return []guest.ServiceInfo{}, nil
}

func serviceAlive(_ string) (bool, string) {
	return false, "services are windows-only"
}

func serviceAction(_ guest.ActionRequest) (guest.ActionResult, *guest.FrameError) {
	return guest.ActionResult{}, &guest.FrameError{Code: "not_supported", Message: "windows services only"}
}
