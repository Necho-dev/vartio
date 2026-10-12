//go:build windows

package agent

import (
	"os/exec"

	"vartio/internal/entities"
)

func doPower(action string) (entities.PowerResult, *entities.FrameError) {
	var args []string
	switch action {
	case "shutdown":
		args = []string{"/s", "/t", "0", "/f"}
	case "reboot":
		args = []string{"/r", "/t", "0", "/f"}
	default:
		return entities.PowerResult{}, bad("action must be shutdown or reboot")
	}
	cmd := exec.Command("shutdown.exe", args...)
	quiet(cmd)
	if err := cmd.Start(); err != nil {
		return entities.PowerResult{}, asRPC(err)
	}
	return entities.PowerResult{Accepted: true, Action: action}, nil
}
