//go:build linux || android || aix || solaris || illumos || zos

package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

func stderrIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stderr.Fd()), unix.TCGETS)
	return err == nil
}
