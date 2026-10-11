//go:build darwin

package main

import (
	"strings"

	"golang.org/x/sys/unix"
)

func firmware() (vendor, product string) {
	model, err := unix.Sysctl("hw.model")
	if err != nil {
		return "", ""
	}
	model = strings.TrimSpace(model)
	if strings.Contains(strings.ToLower(model), "vmware") {
		return "VMware, Inc.", model
	}
	return "Apple", model
}
