//go:build darwin

package agent

import "golang.org/x/sys/unix"

func coreKinds(n int) []string {
	p, err1 := unix.SysctlUint32("hw.perflevel0.logicalcpu")
	e, err2 := unix.SysctlUint32("hw.perflevel1.logicalcpu")
	if err1 != nil || err2 != nil {
		return nil
	}
	return perfKinds(n, int(p), int(e))
}
