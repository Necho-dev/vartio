//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
)

func startPTY(cols, rows int, onOut func([]byte)) (io.WriteCloser, func(), error) {
	cmd := exec.Command("cmd.exe")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
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
		_ = stdin.Close()
	}
	_ = cols
	_ = rows
	return stdin, closer, nil
}
