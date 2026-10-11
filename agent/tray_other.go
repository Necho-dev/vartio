//go:build !darwin && !windows

package agent

import (
	"context"
	"fmt"
	"os"
)

// Linux agents run under systemd; there is no tray.
func trayWanted() bool { return false }

func runTray(context.Context, config, string) {}

func attachConsole() {}

func singleInstance() error { return nil }

func notifyUser(msg string) { fmt.Fprintln(os.Stderr, msg) }

func logToFileByDefault() bool { return false }

func runInstall() int {
	fmt.Fprintln(os.Stderr, "Linux 请用 systemd 管理 vartio-agent（见 supplemental/guides/guest-agent.md）")
	return 1
}

func runUninstall() int { return runInstall() }
