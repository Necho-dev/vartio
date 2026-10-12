//go:build windows

package agent

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// quiet keeps a console-subsystem child (nvidia-smi, cmd, powershell) from
// opening a window. The agent is built -H windowsgui, so Windows would
// otherwise allocate a new console for every sample.
func quiet(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
