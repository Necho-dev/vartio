//go:build windows

package agent

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"fyne.io/systray"
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
)

var (
	//go:embed trayicons/win-ok.ico
	winOK []byte
	//go:embed trayicons/win-off.ico
	winOff []byte
	//go:embed trayicons/win-up.ico
	winUp []byte

	procAttachConsole = modKernel32.NewProc("AttachConsole")
	procMessageBox    = modUser32.NewProc("MessageBoxW")
)

func setTrayIcon(state string) {
	systray.SetIcon(map[string][]byte{"ok": winOK, "off": winOff, "up": winUp}[state])
}

// trayBeforeExit removes the notification-area icon so no stale one lingers
// after the process is replaced.
func trayBeforeExit() {
	systray.Quit()
	time.Sleep(300 * time.Millisecond)
}

// trayWanted: everything except the service runs in the background with a
// tray icon (the binary is built with -H windowsgui, so there is no console).
func trayWanted() bool {
	if getenv("VARTIO_AGENT_TRAY", "GUEST_TRAY") == "off" {
		return false
	}
	ok, _ := svc.IsWindowsService()
	return !ok
}

func stdHandleOK(n uint32) bool {
	h, err := windows.GetStdHandle(n)
	return err == nil && h != 0 && h != windows.InvalidHandle
}

// attachConsole makes CLI subcommands print into the terminal that started
// them; a GUI-subsystem binary has no console of its own. Redirected or piped
// output (the upgrade self-test) already has handles and is left alone.
func attachConsole() {
	if stdHandleOK(windows.STD_OUTPUT_HANDLE) {
		return
	}
	const attachParent = ^uintptr(0)
	if r, _, _ := procAttachConsole.Call(attachParent); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
		fmt.Println()
	}
}

func stderrIsTerminal() bool {
	if !stdHandleOK(windows.STD_ERROR_HANDLE) {
		return false
	}
	h, _ := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	var mode uint32
	return windows.GetConsoleMode(h, &mode) == nil
}

// The service and the tray have no console; log to a file unless stderr was
// redirected somewhere on purpose.
func logToFileByDefault() bool { return !stdHandleOK(windows.STD_ERROR_HANDLE) }

func notifyUser(msg string) {
	t, _ := windows.UTF16PtrFromString("probe-agent")
	m, _ := windows.UTF16PtrFromString(msg)
	const mbIconInformation = 0x40
	_, _, _ = procMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconInformation)
}

func openURL(u string) { _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start() }

func openPath(p string) { _ = exec.Command("notepad.exe", p).Start() }

const mutexName = `Local\probe-agent`

var instance windows.Handle

func singleInstance() error {
	if serviceState() == svc.Running {
		return errors.New("探针已作为 Windows 服务运行，无需再启动")
	}
	name, _ := windows.UTF16PtrFromString(mutexName)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return errors.New("探针已在后台运行（见任务栏右下角托盘图标）")
	}
	instance = h
	return nil
}

func trayRunning() bool {
	name, _ := windows.UTF16PtrFromString(mutexName)
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

// serviceState queries the agent service with the minimum rights a normal
// user has; 0 means not installed or not readable.
func serviceState() svc.State {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(serviceName)
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(s, &st) != nil {
		return svc.Stopped
	}
	return svc.State(st.CurrentState)
}

const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "ProbeAgent"
)

func autostartRegistered() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

// runInstall registers the tray agent to start at login for the current user
// and starts it now. Tray and service are alternatives, not both.
func runInstall() int {
	if serviceState() != 0 {
		fmt.Fprintln(os.Stderr, "已安装为 Windows 服务。托盘模式与服务二选一，请先以管理员执行：vartio-agent.exe service remove")
		return 1
	}
	exe, err := selfExe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if saved, err := saveEnvFile(exe); err != nil {
		fmt.Fprintln(os.Stderr, "保存配置失败：", err)
		return 1
	} else if saved != "" {
		fmt.Println("已保存配置：", saved)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	err = k.SetStringValue(runValue, `"`+exe+`"`)
	k.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("已设置登录自启（当前用户）")
	if trayRunning() {
		fmt.Println("探针已在后台运行，见任务栏右下角托盘图标")
		return 0
	}
	if _, err := spawn(exe, nil, filepath.Dir(exe), windows.DETACHED_PROCESS); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败：", err)
		return 1
	}
	fmt.Println("探针已在后台启动，见任务栏右下角托盘图标；日志：", agentLogFile(filepath.Dir(exe)))
	return 0
}

// runUninstall removes the login item and stops running tray agents of this binary.
func runUninstall() int {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		err = k.DeleteValue(runValue)
		k.Close()
		if err != nil && !errors.Is(err, registry.ErrNotExist) {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	exe, _ := selfExe()
	n := 0
	if procs, err := process.Processes(); err == nil {
		for _, p := range procs {
			if int(p.Pid) == os.Getpid() {
				continue
			}
			if args, _ := p.CmdlineSlice(); len(args) > 1 && args[1] != "" {
				continue // service run, upgrade-watch, CLI subcommands
			}
			if path, err := p.Exe(); err == nil && strings.EqualFold(path, exe) {
				if p.Kill() == nil {
					n++
				}
			}
		}
	}
	fmt.Printf("已取消登录自启，停止了 %d 个后台探针\n", n)
	return 0
}
