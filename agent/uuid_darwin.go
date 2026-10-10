//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

func runIORegUUID() (string, error) {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		parts := strings.Split(line, "=")
		if len(parts) < 2 {
			continue
		}
		return strings.Trim(strings.TrimSpace(parts[1]), `"`), nil
	}
	return "", errNoSMBIOS
}
