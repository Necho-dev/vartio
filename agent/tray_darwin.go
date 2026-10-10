//go:build darwin

package main

import (
	_ "embed"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/unix"
)

var (
	//go:embed trayicons/mac-ok.png
	macOK []byte
	//go:embed trayicons/mac-off.png
	macOff []byte
	//go:embed trayicons/mac-up.png
	macUp []byte
)

// trayFallbackPosition puts a first-run icon just left of the system items
// (clock, Control Center) so the notch cannot hide it; ⌘-drag moves it and the
// new place survives restarts and upgrades.
const trayFallbackPosition = 250

func init() { systray.SetAutosaveName(launchLabel, trayFallbackPosition) }

// trayBeforeExit drops the status item ahead of an in-place exec; otherwise the
// new image inherits the old item and ignores the saved position.
func trayBeforeExit() { systray.RemoveStatusItem() }

func setTrayIcon(state string) {
	icon := map[string][]byte{"ok": macOK, "off": macOff, "up": macUp}[state]
	systray.SetTemplateIcon(icon, icon)
}

// trayWanted: a menu bar item needs the user's GUI session, so not as root
// (LaunchDaemon) and not from a plain SSH login.
func trayWanted() bool {
	if os.Getenv("GUEST_TRAY") == "off" || os.Geteuid() == 0 {
		return false
	}
	return os.Getenv("SSH_CONNECTION") == "" || runnerName() == "launchd"
}

func attachConsole() {}

func stderrIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stderr.Fd()), unix.TIOCGETA)
	return err == nil
}

// Under launchd nobody reads stderr; in a terminal keep printing there too.
func logToFileByDefault() bool { return trayMode && !stderrIsTerminal() }

func notifyUser(msg string) { fmt.Fprintln(os.Stderr, msg) }

func openURL(u string) { _ = exec.Command("open", u).Start() }

func openPath(p string) {
	if exec.Command("open", "-a", "Console", p).Run() != nil {
		_ = exec.Command("open", "-t", p).Start()
	}
}

var lockFile *os.File

func lockPath() string {
	if exe, err := selfExe(); err == nil {
		return filepath.Join(filepath.Dir(exe), "probe-agent.lock")
	}
	return filepath.Join(os.TempDir(), "probe-agent.lock")
}

// singleInstance holds an flock for the life of the process. The fd is
// close-on-exec, so an in-place upgrade releases it and the new image retakes it.
func singleInstance() error {
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil // no lock is better than refusing to run
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if runnerName() != "launchd" {
			f.Close()
			return errors.New("探针已在后台运行（见菜单栏图标）")
		}
		// KeepAlive would relaunch us every few seconds; wait for the other one instead.
		fmt.Fprintln(os.Stderr, "probe-agent: another instance holds the lock, waiting")
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return err
		}
	}
	_ = f.Truncate(0)
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	lockFile = f
	return nil
}

const launchLabel = "probe-agent"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}

func launchTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// runInstall registers a per-user LaunchAgent: started at login and restarted
// whenever it exits; only uninstall (bootout) stops it.
func runInstall() int {
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "请以登录用户身份执行（不要 sudo）：菜单栏图标只在用户会话里可见")
		return 1
	}
	exe, err := selfExe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plist, err := plistPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = exec.Command("launchctl", "bootout", launchTarget()+"/"+launchLabel).Run()
	if !lockFree() {
		fmt.Fprintln(os.Stderr, "已有探针在运行（多半是终端里启动的），请先停止它再执行 install")
		return 1
	}
	if saved, err := saveEnvFile(exe); err != nil {
		fmt.Fprintln(os.Stderr, "保存配置失败：", err)
		return 1
	} else if saved != "" {
		fmt.Println("已保存配置：", saved)
	}
	dir := filepath.Dir(exe)
	esc := html.EscapeString
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + launchLabel + `</string>
	<key>ProgramArguments</key><array><string>` + esc(exe) + `</string></array>
	<key>WorkingDirectory</key><string>` + esc(dir) + `</string>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ProcessType</key><string>Interactive</string>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>StandardErrorPath</key><string>` + esc(filepath.Join(dir, "probe-agent.log")) + `</string>
</dict>
</plist>
`
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if out, err := exec.Command("launchctl", "bootstrap", launchTarget(), plist).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "launchctl bootstrap: %v %s\n", err, out)
		return 1
	}
	fmt.Println("已设置登录自启：探针在后台运行，菜单栏可见图标")
	fmt.Println("  LaunchAgent：", plist)
	fmt.Println("  日志：", filepath.Join(dir, "probe-agent.log"))
	return 0
}

func runUninstall() int {
	plist, err := plistPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = exec.Command("launchctl", "bootout", launchTarget()+"/"+launchLabel).Run()
	if err := os.Remove(plist); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("已取消登录自启并停止后台探针")
	return 0
}

// lockFree waits briefly for a stopping agent to release the instance lock.
func lockFree() bool {
	for range 50 {
		f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return true
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		f.Close()
		if err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
