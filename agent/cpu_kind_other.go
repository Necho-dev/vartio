//go:build !darwin && !linux

package agent

func coreKinds(int) []string { return nil }
