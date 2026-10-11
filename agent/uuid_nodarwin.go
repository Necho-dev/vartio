//go:build !windows && !darwin

package agent

func runIORegUUID() (string, error) { return "", errNoSMBIOS }
