//go:build !windows

package agent

import (
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTY(cols, rows int, onOut func([]byte), onExit func()) (io.WriteCloser, func(), func(int, int), error) {
	shell := getenv("VARTIO_AGENT_TERM_SHELL", "GUEST_TERM_SHELL")
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/bash"
	}
	name, args := shell, []string{"-i"}
	if hostNS() {
		// The container's $SHELL may not exist on the host.
		name, args = hostCommand("/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash -l; exec sh -l")
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"DISABLE_AUTO_UPDATE=true",
	)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, nil, nil, err
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				p := make([]byte, n)
				copy(p, buf[:n])
				onOut(p)
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
		onExit()
	}()
	closer := func() {
		_ = cmd.Process.Kill()
		_ = f.Close()
	}
	resize := func(c, r int) {
		_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(c), Rows: uint16(r)})
	}
	return f, closer, resize, nil
}
