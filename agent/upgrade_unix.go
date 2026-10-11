//go:build !windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func runnerName() string {
	switch {
	case inContainer():
		return "docker"
	case os.Getenv("INVOCATION_ID") != "":
		return "systemd"
	case os.Getenv("XPC_SERVICE_NAME") != "" && os.Getenv("XPC_SERVICE_NAME") != "0":
		return "launchd"
	}
	return "console"
}

// applyUpgrade replaces this process in place (same PID, same supervisor).
func applyUpgrade(st *upgradeState, staged string) error {
	if err := swapIn(st.Exe, staged); err != nil {
		return err
	}
	st.PID = os.Getpid()
	path := statePath(st.Exe)
	if err := writeState(path, st); err != nil {
		_ = swapBack(st.Exe)
		return err
	}
	if err := spawnDetached(st.Exe+".old", []string{"upgrade-watch", path}, st.Cwd); err != nil {
		_ = swapBack(st.Exe)
		return err
	}
	beforeExit()
	err := syscall.Exec(st.Exe, append([]string{st.Exe}, st.Args...), os.Environ())
	_ = swapBack(st.Exe)
	return err
}

func spawnDetached(bin string, args []string, dir string) error {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if f, err := os.OpenFile(agentLogFile(filepath.Dir(bin)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
		defer f.Close()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

type unixWatcher struct{ st *upgradeState }

func newWatcher(st *upgradeState) watcher { return unixWatcher{st} }

// start: the agent already exec'd into the new build.
func (w unixWatcher) start() error { return nil }

// stop uses SIGKILL so systemd's Restart=on-failure also brings the agent back.
func (w unixWatcher) stop() {
	_ = syscall.Kill(w.st.PID, syscall.SIGKILL)
	for i := 0; i < 50 && syscall.Kill(w.st.PID, 0) == nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

// restart leaves supervised agents to their supervisor and relaunches the rest.
func (w unixWatcher) restart() error {
	if w.st.Runner != "console" {
		return nil
	}
	if !errors.Is(syscall.Kill(w.st.PID, 0), syscall.ESRCH) {
		return errors.New("new version still running")
	}
	return spawnDetached(w.st.Exe, w.st.Args, w.st.Cwd)
}
