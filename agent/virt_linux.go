//go:build linux

package main

import (
	"os"
	"strings"
)

func firmware() (vendor, product string) {
	return readID("/sys/class/dmi/id/sys_vendor"), readID("/sys/class/dmi/id/product_name")
}

func readID(path string) string {
	b, err := os.ReadFile(hostPath(path))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if s == "" || strings.Contains(strings.ToLower(s), "to be filled") {
		return ""
	}
	return s
}
