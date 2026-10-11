//go:build !windows

package agent

import (
	"os/exec"

	"vartio/internal/entities"
)

func doPower(action string) (entities.PowerResult, *entities.FrameError) {
	if getenv("VARTIO_AGENT_ALLOW_POWER", "GUEST_ALLOW_POWER") != "1" {
		return entities.PowerResult{}, &entities.FrameError{Code: "not_supported", Message: "power is windows-only unless GUEST_ALLOW_POWER=1"}
	}
	flag := map[string]string{"shutdown": "-h", "reboot": "-r"}[action]
	if flag == "" {
		return entities.PowerResult{}, bad("action must be shutdown or reboot")
	}
	name, args := hostCommand("shutdown", flag, "now")
	_ = exec.Command(name, args...).Start()
	return entities.PowerResult{Accepted: true, Action: action}, nil
}
