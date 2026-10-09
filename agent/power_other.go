//go:build !windows

package main

import (
	"os"
	"os/exec"

	"govmomi-wmks/internal/guest"
)

func doPower(action string) (guest.PowerResult, *guest.FrameError) {
	if os.Getenv("GUEST_ALLOW_POWER") != "1" {
		return guest.PowerResult{}, &guest.FrameError{Code: "not_supported", Message: "power is windows-only unless GUEST_ALLOW_POWER=1"}
	}
	switch action {
	case "shutdown":
		_ = exec.Command("shutdown", "-h", "now").Start()
	case "reboot":
		_ = exec.Command("shutdown", "-r", "now").Start()
	default:
		return guest.PowerResult{}, bad("action must be shutdown or reboot")
	}
	return guest.PowerResult{Accepted: true, Action: action}, nil
}
