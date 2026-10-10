//go:build !darwin && !windows

package main

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Linux agents run under systemd; there is no tray.
func trayWanted() bool { return false }

func runTray(context.Context, config, string) {}

func attachConsole() {}

func singleInstance() error { return nil }

func notifyUser(msg string) { fmt.Fprintln(os.Stderr, msg) }

func logToFileByDefault() bool { return false }

func stderrIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stderr.Fd()), unix.TCGETS)
	return err == nil
}

func runInstall() int {
	fmt.Fprintln(os.Stderr, "Linux 请用 systemd 管理 probe-agent（见 docs/guest-agent.md）")
	return 1
}

func runUninstall() int { return runInstall() }
