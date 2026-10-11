//go:build freebsd || openbsd || netbsd || dragonfly

package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

func stderrIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stderr.Fd()), unix.TIOCGETA)
	return err == nil
}
