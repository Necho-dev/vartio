package agent

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// trayMode is set once at startup: the agent runs in the background with a
// tray / menu bar icon instead of a terminal.
var trayMode bool

// beforeExit lets the tray remove its icon before the process exits or execs for an upgrade.
var beforeExit = func() {}

// linkState is what the tray shows about the hub connection.
type linkState struct {
	Connected bool
	Dialing   bool
	Since     time.Time
	Err       string
	Upgrading string // version being staged or restarted into
	Probation string // version running on probation until the hub accepts it
}

var link = struct {
	mu      sync.Mutex
	s       linkState
	changed chan struct{}
}{s: linkState{Dialing: true}, changed: make(chan struct{}, 1)}

func setLink(fn func(*linkState)) {
	link.mu.Lock()
	fn(&link.s)
	link.mu.Unlock()
	select {
	case link.changed <- struct{}{}:
	default:
	}
}

func getLink() linkState {
	link.mu.Lock()
	defer link.mu.Unlock()
	return link.s
}

var reconnectReq = make(chan struct{}, 1)

// requestReconnect drops the current session (or skips the backoff wait).
func requestReconnect() {
	select {
	case reconnectReq <- struct{}{}:
	default:
	}
}

const logMax = 10 << 20

// rotLog is an append-only log file that keeps one previous generation (.1).
type rotLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openLog(path string) (*rotLog, error) {
	l := &rotLog{path: path}
	return l, l.open()
}

func (l *rotLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *rotLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(p)) > logMax {
		_ = l.f.Close()
		_ = os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// logPath is the log next to the binary, or the user cache dir when that
// directory is not writable. An existing probe-agent.log stays in use.
func logPath() string {
	if exe, err := selfExe(); err == nil {
		dir := filepath.Dir(exe)
		p := agentLogFile(dir)
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.Close()
			return p
		}
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	legacy := filepath.Join(dir, "probe-agent")
	if st, err := os.Stat(legacy); err == nil && st.IsDir() {
		dir = legacy
	} else {
		dir = filepath.Join(dir, "vartio")
	}
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "vartio-agent.log")
}

func agentLogFile(dir string) string {
	return existingFile(filepath.Join(dir, "vartio-agent.log"), filepath.Join(dir, "probe-agent.log"))
}

// setupLog sends the log to a file when nobody is watching stderr.
// GUEST_LOG=stderr forces stderr, any other value is a file path.
func setupLog() string {
	path := getenv("VARTIO_AGENT_LOG", "GUEST_LOG")
	switch {
	case path == "stderr":
		return ""
	case path == "" && !logToFileByDefault():
		return ""
	case path == "":
		path = logPath()
	}
	w, err := openLog(path)
	if err != nil {
		log.Printf("log file %s: %v", path, err)
		return ""
	}
	var out io.Writer = w
	if stderrIsTerminal() {
		out = io.MultiWriter(w, os.Stderr)
	}
	log.SetOutput(out)
	return path
}

// ago renders a duration the way the tray menu shows it.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时 %d 分钟", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%d 天 %d 小时", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
}

// trayText is the status line and icon state for s.
func trayText(s linkState) (string, string) {
	switch {
	case s.Upgrading != "":
		return "正在升级到 " + s.Upgrading + "…", "up"
	case s.Probation != "" && !s.Connected:
		return "已升级到 " + s.Probation + "，等待连上 hub 确认…", "up"
	case s.Connected:
		return "已连接 · " + ago(time.Since(s.Since)), "ok"
	case s.Dialing || s.Err == "":
		return "连接中…", "off"
	}
	msg := s.Err
	if r := []rune(msg); len(r) > 60 {
		msg = string(r[:60]) + "…"
	}
	return "未连接：" + msg, "off"
}
