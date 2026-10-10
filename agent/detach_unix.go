//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session so it outlives agent restarts and
// upgrades, and is not signalled with the agent's process group.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
