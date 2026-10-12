//go:build darwin

package agent

import (
	"context"
	"os/exec"
	"time"

	"vartio/internal/entities"
)

func listPorts() ([]entities.PortInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// -sTCP:LISTEN limits TCP; -iUDP is a separate selection, so both come back.
	b, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "+c", "0", "-nP", "-iTCP", "-sTCP:LISTEN", "-iUDP", "-FPcn").Output()
	if err != nil && len(b) == 0 {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return []entities.PortInfo{}, nil
		}
		return nil, err
	}
	return parseLsofPorts(b), nil
}
