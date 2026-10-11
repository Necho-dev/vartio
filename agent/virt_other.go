//go:build !linux && !darwin && !windows

package agent

func firmware() (vendor, product string) { return "", "" }
