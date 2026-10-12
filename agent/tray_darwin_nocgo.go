//go:build darwin && !cgo

package agent

import "context"

// A CGO_ENABLED=0 build does not link AppKit. The menu bar is the cgo build's option.
func trayLinked() bool { return false }

func runTray(context.Context, config, string) {}
