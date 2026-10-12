//go:build !windows

package agent

import "os/exec"

func quiet(cmd *exec.Cmd) {}
