//go:build !windows

package main

import (
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTY(cols, rows int, onOut func([]byte)) (io.WriteCloser, func(), error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell, "-i")
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"DISABLE_AUTO_UPDATE=true",
	)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, nil, err
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
				return
			}
		}
	}()
	closer := func() {
		_ = cmd.Process.Kill()
		_ = f.Close()
	}
	return f, closer, nil
}
