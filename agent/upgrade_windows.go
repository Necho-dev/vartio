//go:build windows

package agent

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func runnerName() string {
	if ok, _ := svc.IsWindowsService(); ok {
		return "service"
	}
	if trayMode {
		return "tray"
	}
	return "console"
}

// applyUpgrade swaps the binary (a running exe may be renamed, not overwritten)
// and hands the restart to the watchdog, which runs from the previous binary.
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
	if _, err := spawn(st.Exe+".old", []string{"upgrade-watch", path}, st.Cwd, windows.DETACHED_PROCESS); err != nil {
		_ = swapBack(st.Exe)
		return err
	}
	if st.Runner == "service" {
		return nil // the watchdog stops and restarts the service
	}
	beforeExit()
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
	return nil
}

// restartInPlace is not offered on Windows: a process cannot replace itself,
// and the corruption it recovers from has only been seen on macOS.
func restartInPlace() error {
	return errors.New("restart in place is not supported on windows")
}

func spawn(bin string, args []string, dir string, flags uint32) (int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}

func newWatcher(st *upgradeState) watcher {
	if st.Runner == "service" {
		return serviceWatcher{}
	}
	return &consoleWatcher{st: st}
}

type serviceWatcher struct{}

func (serviceWatcher) start() error {
	if err := controlService(false); err != nil {
		return err
	}
	return controlService(true)
}
func (serviceWatcher) stop()          { _ = controlService(false) }
func (serviceWatcher) restart() error { return controlService(true) }

// controlService stops (start=false) or starts the agent service and waits for it.
func controlService(start bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	want := svc.Stopped
	if start {
		want = svc.Running
		if err := s.Start(); err != nil {
			return err
		}
	} else if _, err := s.Control(svc.Stop); err != nil {
		if q, qerr := s.Query(); qerr == nil && q.State == svc.Stopped {
			return nil
		}
		return err
	}
	for range 60 {
		q, err := s.Query()
		if err != nil {
			return err
		}
		if q.State == want {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("service did not reach state %d", want)
}

type consoleWatcher struct {
	st  *upgradeState
	pid int
}

// start waits for the old agent to exit so the new one is not refused as a duplicate.
func (w *consoleWatcher) start() error {
	if !waitExit(w.st.PID, 30*time.Second) {
		kill(w.st.PID)
	}
	return w.launch()
}

func (w *consoleWatcher) launch() error {
	flags := uint32(windows.DETACHED_PROCESS)
	if w.st.Runner == "console" {
		flags = windows.CREATE_NEW_CONSOLE
	}
	pid, err := spawn(w.st.Exe, w.st.Args, w.st.Cwd, flags)
	if err != nil {
		return err
	}
	w.pid = pid
	log.Printf("watch: started pid %d", w.pid)
	return nil
}

func (w *consoleWatcher) stop() {
	if w.pid != 0 {
		kill(w.pid)
		waitExit(w.pid, 10*time.Second)
	}
}

func (w *consoleWatcher) restart() error { return w.launch() }

func waitExit(pid int, d time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // already gone
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

func kill(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			log.Printf("watch: open pid %d: %v", pid, err)
		}
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.TerminateProcess(h, 1)
}
