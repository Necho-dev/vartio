//go:build !windows

package main

import (
	"os"
	"runtime"
	"strings"
)

func readBIOSUUID() (string, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/sys/class/dmi/id/product_uuid")
		if err == nil {
			return strings.ToLower(strings.TrimSpace(string(b))), nil
		}
	}
	if runtime.GOOS == "darwin" {
		out, err := runIORegUUID()
		if err == nil && out != "" {
			return strings.ToLower(out), nil
		}
	}
	return "", errNoSMBIOS
}
