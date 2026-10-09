//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startPTY runs the shell under a ConPTY (Windows 10 1809+) so echo, line editing,
// colors and resize behave like a real console. Older systems fall back to pipes.
func startPTY(cols, rows int, onOut func([]byte), onExit func()) (io.WriteCloser, func(), func(int, int), error) {
	shell := os.Getenv("GUEST_TERM_SHELL")
	if shell == "" {
		shell = "powershell.exe -NoLogo"
	}
	w, closer, resize, err := startConPTY(shell, cols, rows, onOut, onExit)
	if err == nil {
		return w, closer, resize, nil
	}
	return startPipes(onOut, onExit)
}

func startConPTY(cmdline string, cols, rows int, onOut func([]byte), onExit func()) (io.WriteCloser, func(), func(int, int), error) {
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, nil, nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, nil, nil, err
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(coord(cols, rows), inR, outW, 0, &hpc); err != nil {
		for _, h := range []windows.Handle{inR, inW, outR, outW} {
			windows.CloseHandle(h)
		}
		return nil, nil, nil, err
	}
	// The pseudo console holds its own references to these ends.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)

	fail := func(err error) (io.WriteCloser, func(), func(int, int), error) {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, nil, nil, err
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return fail(err)
	}
	defer attrs.Delete()
	// The attribute value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return fail(err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// Without STARTF_USESTDHANDLES the child inherits our redirected std handles
	// instead of the pseudo console.
	si.Flags = windows.STARTF_USESTDHANDLES
	cmd, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return fail(err)
	}
	var cwd *uint16
	if home, err := os.UserHomeDir(); err == nil {
		cwd, _ = windows.UTF16PtrFromString(home)
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmd, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		nil, cwd, &si.StartupInfo, &pi); err != nil {
		return fail(err)
	}
	windows.CloseHandle(pi.Thread)

	go func() {
		buf := make([]byte, 8192)
		for {
			var n uint32
			err := windows.ReadFile(outR, buf, &n, nil)
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

	var once sync.Once
	closer := func() {
		once.Do(func() {
			_ = windows.TerminateProcess(pi.Process, 1)
			windows.ClosePseudoConsole(hpc)
			windows.CloseHandle(inW)
			windows.CloseHandle(outR)
			windows.CloseHandle(pi.Process)
		})
	}
	go func() {
		_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		closer()
		onExit()
	}()
	resize := func(c, r int) {
		_ = windows.ResizePseudoConsole(hpc, coord(c, r))
	}
	return &handleWriter{h: inW}, closer, resize, nil
}

func coord(cols, rows int) windows.Coord {
	return windows.Coord{X: int16(cols), Y: int16(rows)}
}

type handleWriter struct{ h windows.Handle }

func (w *handleWriter) Write(p []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(w.h, p, &n, nil)
	return int(n), err
}

func (w *handleWriter) Close() error { return nil }

func startPipes(onOut func([]byte), onExit func()) (io.WriteCloser, func(), func(int, int), error) {
	cmd := exec.Command("cmd.exe")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, errors.Join(errors.New("conpty unavailable"), err)
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
				break
			}
		}
		_ = cmd.Wait()
		onExit()
	}()
	closer := func() {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
	}
	return stdin, closer, func(int, int) {}, nil
}
