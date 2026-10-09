//go:build !windows

package main

import (
	"fmt"
	"os"
)

func runServiceCommand(args []string) int {
	fmt.Fprintln(os.Stderr, "service install/remove is windows-only")
	_ = args
	return 2
}
