//go:build windows

package main

import (
	"os/exec"

	"govmomi-wmks/internal/guest"
)

func doPower(action string) (guest.PowerResult, *guest.FrameError) {
	var args []string
	switch action {
	case "shutdown":
		args = []string{"/s", "/t", "0", "/f"}
	case "reboot":
		args = []string{"/r", "/t", "0", "/f"}
	default:
		return guest.PowerResult{}, bad("action must be shutdown or reboot")
	}
	cmd := exec.Command("shutdown.exe", args...)
	if err := cmd.Start(); err != nil {
		return guest.PowerResult{}, asRPC(err)
	}
	return guest.PowerResult{Accepted: true, Action: action}, nil
}
