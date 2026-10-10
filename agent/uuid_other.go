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

func readHardwareUUID() string {
	id, _ := readBIOSUUID()
	return id
}

// readMachineID is the OS install id (systemd machine-id; on macOS the platform UUID,
// as there is no separate install id).
func readMachineID() string {
	if runtime.GOOS == "darwin" {
		id, _ := runIORegUUID()
		return id
	}
	for _, p := range []string{hostPath("/etc/machine-id"), hostPath("/var/lib/dbus/machine-id")} {
		if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
	}
	if inContainer() {
		return containerMachineID()
	}
	return ""
}
