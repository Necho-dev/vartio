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
	flag := map[string]string{"shutdown": "-h", "reboot": "-r"}[action]
	if flag == "" {
		return guest.PowerResult{}, bad("action must be shutdown or reboot")
	}
	name, args := hostCommand("shutdown", flag, "now")
	_ = exec.Command(name, args...).Start()
	return guest.PowerResult{Accepted: true, Action: action}, nil
}
