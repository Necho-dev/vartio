package guest

import (
	"encoding/json"
	"time"
)

const (
	MethodHello          = "hello"
	MethodHealth         = "health"
	MethodProcsList      = "procs.list"
	MethodProcsAction    = "procs.action"
	MethodServicesList   = "services.list"
	MethodServicesAction = "services.action"
	MethodExec           = "exec"
	MethodTermOpen       = "term.open"
	MethodTermInput      = "term.input"
	MethodTermResize     = "term.resize"
	MethodTermClose      = "term.close"
	MethodPower          = "power"
	MethodDisplayOpen    = "display.open"
	MethodDisplayClose   = "display.close"
	MethodFilesList      = "files.list"
	MethodFilesGet       = "files.get"
	MethodFilesPut       = "files.put"
)

const (
	HealthTimeout   = 5 * time.Second
	ListTimeout     = 5 * time.Second
	ActionTimeout   = 15 * time.Second
	PowerTimeout    = 10 * time.Second
	TermOpenTimeout = 10 * time.Second
	ExecDefault     = 30 * time.Second
	ExecMax         = 120 * time.Second
	TermIdle        = 15 * time.Minute
	DisplayIdle     = 15 * time.Minute
	TicketTTL       = 2 * time.Minute
	PingEvery       = 20 * time.Second
	PongWait        = 45 * time.Second
	FileMaxBytes    = 512 << 20
	FileChunk       = 256 << 10
	FileChunkWait   = 20 * time.Second
	DisplayFPS      = 2
	DisplayMaxSide  = 1920
	DisplayJPEGQ    = 88
)

// Frame is one JSON object on the outbound agent WebSocket.
type Frame struct {
	ID     string          `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *FrameError     `json:"error,omitempty"`
	Stream string          `json:"stream,omitempty"`
	Data   []byte          `json:"data,omitempty"`
}

type FrameError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *FrameError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type Hello struct {
	BiosUUID string   `json:"biosUuid"`
	HostName string   `json:"hostname,omitempty"`
	Version  string   `json:"version,omitempty"`
	MACs     []string `json:"macs,omitempty"`
}

type HelloOK struct {
	OK bool `json:"ok"`
}

type Health struct {
	TimeUTC string     `json:"timeUtc"`
	CPU     CPUInfo    `json:"cpu"`
	Memory  MemInfo    `json:"memory"`
	Disks   []DiskInfo `json:"disks"`
	NICs    []NICInfo  `json:"nics"`
	Net     NetRates   `json:"guestNet"`
	Watches []Watch    `json:"watches,omitempty"`
}

type CPUInfo struct {
	Model       string  `json:"model,omitempty"`
	LogicalCPUs int     `json:"logicalCpus"`
	Pct         float64 `json:"pct"`
}

type MemInfo struct {
	TotalMB int64   `json:"totalMB"`
	AvailMB int64   `json:"availMB"`
	Pct     float64 `json:"pct"`
}

type DiskInfo struct {
	Name    string  `json:"name"`
	TotalGB float64 `json:"totalGB"`
	UsedGB  float64 `json:"usedGB"`
	Pct     float64 `json:"pct"`
}

type NICInfo struct {
	Name     string `json:"name"`
	MAC      string `json:"mac,omitempty"`
	IPv4     string `json:"ipv4,omitempty"`
	LinkMbps int    `json:"linkMbps,omitempty"`
	Up       bool   `json:"up"`
}

type NetRates struct {
	RxMbps float64 `json:"rxMbps"`
	TxMbps float64 `json:"txMbps"`
}

type Watch struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type ProcInfo struct {
	PID     int32   `json:"pid"`
	Name    string  `json:"name"`
	CPUPct  float64 `json:"cpuPct,omitempty"`
	MemMB   float64 `json:"memMB,omitempty"`
	Status  string  `json:"status,omitempty"`
	Cmdline string  `json:"cmdline,omitempty"`
}

type ServiceInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	PID    int32  `json:"pid,omitempty"`
}

type ActionRequest struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

type ActionResult struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
}

type ExecRequest struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Shell      string            `json:"shell,omitempty"`
	TimeoutSec int               `json:"timeoutSec,omitempty"`
}

type ExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut"`
}

type PowerRequest struct {
	Action string `json:"action"`
}

type PowerResult struct {
	Accepted bool   `json:"accepted"`
	Action   string `json:"action"`
	Message  string `json:"message,omitempty"`
}

type TermOpenRequest struct {
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
}

type TermOpenResult struct {
	OK bool `json:"ok"`
}

type SessionInfo struct {
	Online      bool     `json:"online"`
	LastSeenUTC string   `json:"lastSeenUtc,omitempty"`
	Version     string   `json:"version,omitempty"`
	BiosUUID    string   `json:"biosUuid,omitempty"`
	HostName    string   `json:"hostname,omitempty"`
	MACs        []string `json:"macs,omitempty"`
}

type DisplayOpenResult struct {
	OK      bool `json:"ok"`
	FPS     int  `json:"fps"`
	MaxSide int  `json:"maxSide"`
	Width   int  `json:"width,omitempty"`
	Height  int  `json:"height,omitempty"`
}

type DisplayInfo struct {
	ViewOnly bool   `json:"viewOnly"`
	FPS      int    `json:"fps"`
	MaxSide  int    `json:"maxSide"`
	Note     string `json:"note"`
}

type FilesListRequest struct {
	Path string `json:"path,omitempty"`
}

type FileEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size,omitempty"`
	MtimeUTC string `json:"mtimeUtc,omitempty"`
}

type FilesListResult struct {
	Path    string      `json:"path"`
	Staging string      `json:"staging"`
	Entries []FileEntry `json:"entries"`
}

type FilesGetRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Length int    `json:"length,omitempty"`
	Stat   bool   `json:"stat,omitempty"`
}

type FilesGetResult struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	IsDir  bool   `json:"isDir,omitempty"`
	EOF    bool   `json:"eof"`
	SHA256 string `json:"sha256,omitempty"`
	Data   []byte `json:"data,omitempty"`
}

type FilesPutRequest struct {
	Path    string `json:"path"`
	Offset  int64  `json:"offset"`
	Data    []byte `json:"data,omitempty"`
	EOF     bool   `json:"eof"`
	SHA256  string `json:"sha256,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

type FilesPutResult struct {
	OK     bool   `json:"ok"`
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

type FileMeta struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
	Staging string `json:"staging,omitempty"`
}

type AgentFlag struct {
	Online bool `json:"online"`
}

func TimeoutFor(method string, execSec int) time.Duration {
	switch method {
	case MethodHealth, MethodProcsList, MethodServicesList:
		return ListTimeout
	case MethodProcsAction, MethodServicesAction:
		return ActionTimeout
	case MethodPower:
		return PowerTimeout
	case MethodTermOpen, MethodTermClose, MethodTermResize, MethodDisplayOpen, MethodDisplayClose:
		return TermOpenTimeout
	case MethodFilesList:
		return ListTimeout
	case MethodFilesGet, MethodFilesPut:
		return FileChunkWait
	case MethodExec:
		d := ExecDefault
		if execSec > 0 {
			d = time.Duration(execSec) * time.Second
		}
		if d > ExecMax {
			return ExecMax
		}
		if d < time.Second {
			return time.Second
		}
		return d
	default:
		return ListTimeout
	}
}
