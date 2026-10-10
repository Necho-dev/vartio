package guest

import (
	"encoding/json"
	"time"
)

const (
	MethodHello          = "hello"
	MethodHealth         = "health"
	MethodMetrics        = "metrics"
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
	MethodFilesOp        = "files.op"
	MethodTrash          = "files.trash"
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
	FileOpTimeout   = 10 * time.Minute
	DisplayFPS      = 2
	DisplayMaxSide  = 1920
	DisplayJPEGQ    = 88
	// MaxTerms caps concurrent terminal sessions (browser tabs) per guest.
	MaxTerms = 8
)

// CapMultiTerm in Hello.Caps means the agent keys PTYs by stream id, so several
// terminals can run at once. Agents without it keep a single PTY.
const CapMultiTerm = "term.multi"

// Stream "exit" (agent → hub, Data empty) reports that the shell behind a
// terminal stream id has ended.
const StreamTermExit = "exit"

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

// Hello opens an agent session. DeviceID is the hardware fingerprint the hub keys
// systems by; agents before 1.8 omit it and are keyed by BiosUUID instead.
type Hello struct {
	BiosUUID string       `json:"biosUuid"`
	DeviceID string       `json:"deviceId,omitempty"`
	Device   *DeviceFacts `json:"device,omitempty"`
	HostName string       `json:"hostname,omitempty"`
	Version  string       `json:"version,omitempty"`
	MACs     []string     `json:"macs,omitempty"`
	Caps     []string     `json:"caps,omitempty"`
}

// DeviceFacts are short digests of the fingerprint inputs, so the hub can match a
// changed fingerprint to its old record without ever seeing the raw machine id.
type DeviceFacts struct {
	MID string `json:"mid,omitempty"` // OS install id (machine-id / MachineGuid / IOPlatformUUID)
	HW  string `json:"hw,omitempty"`  // SMBIOS / platform hardware UUID
	MAC string `json:"mac,omitempty"` // primary NIC
}

// Matches counts inputs that are present on both sides and equal.
func (d *DeviceFacts) Matches(o *DeviceFacts) int {
	if d == nil || o == nil {
		return 0
	}
	n := 0
	for _, p := range [][2]string{{d.MID, o.MID}, {d.HW, o.HW}, {d.MAC, o.MAC}} {
		if p[0] != "" && p[0] == p[1] {
			n++
		}
	}
	return n
}

// Key is the session / system key: the fingerprint, or the BIOS UUID for old agents.
func (h Hello) Key() string {
	if h.DeviceID != "" {
		return h.DeviceID
	}
	return h.BiosUUID
}

func (h Hello) Has(c string) bool {
	for _, x := range h.Caps {
		if x == c {
			return true
		}
	}
	return false
}

type HelloOK struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
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

// SysInfo is the static part of a metrics reply.
type SysInfo struct {
	HostName        string `json:"hostname,omitempty"`
	OS              string `json:"os,omitempty"`
	Platform        string `json:"platform,omitempty"`
	PlatformVersion string `json:"platformVersion,omitempty"`
	Kernel          string `json:"kernel,omitempty"`
	Arch            string `json:"arch,omitempty"`
	CPUModel        string `json:"cpuModel,omitempty"`
	Cores           int    `json:"cores,omitempty"`
	Threads         int    `json:"threads,omitempty"`
	BootTime        int64  `json:"bootTime,omitempty"`
}

type DiskUsage struct {
	Mount string `json:"mount"`
	FS    string `json:"fs,omitempty"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type TempSensor struct {
	Name string  `json:"name"`
	C    float64 `json:"c"`
}

// Metrics is one monitoring sample. Byte counters are cumulative; the hub derives rates.
type Metrics struct {
	TimeMs    int64        `json:"timeMs"`
	Info      SysInfo      `json:"info"`
	CPUPct    float64      `json:"cpuPct"`
	Load      [3]float64   `json:"load"`
	MemTotal  uint64       `json:"memTotal"`
	MemUsed   uint64       `json:"memUsed"`
	SwapTotal uint64       `json:"swapTotal"`
	SwapUsed  uint64       `json:"swapUsed"`
	RootMount string       `json:"rootMount,omitempty"`
	Disks     []DiskUsage  `json:"disks"`
	DiskRead  uint64       `json:"diskRead"`
	DiskWrite uint64       `json:"diskWrite"`
	NetRx     uint64       `json:"netRx"`
	NetTx     uint64       `json:"netTx"`
	NICs      []NICInfo    `json:"nics"`
	Temps     []TempSensor `json:"temps,omitempty"`
	Procs     int          `json:"procs"`
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

// TermOpenRequest is also the term.resize payload; ID names the stream to
// resize (empty = every terminal, for single-PTY agents).
type TermOpenRequest struct {
	ID   string `json:"id,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type TermOpenResult struct {
	OK bool `json:"ok"`
}

type SessionInfo struct {
	Online      bool         `json:"online"`
	LastSeenUTC string       `json:"lastSeenUtc,omitempty"`
	Version     string       `json:"version,omitempty"`
	DeviceID    string       `json:"deviceId,omitempty"`
	Device      *DeviceFacts `json:"device,omitempty"`
	BiosUUID    string       `json:"biosUuid,omitempty"`
	HostName    string       `json:"hostname,omitempty"`
	MACs        []string     `json:"macs,omitempty"`
	RemoteAddr  string       `json:"remoteAddr,omitempty"`
	ConnectedAt string       `json:"connectedAt,omitempty"`
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
	Hidden   bool   `json:"hidden,omitempty"`
	Link     bool   `json:"link,omitempty"`
}

// FilesListResult: Path "" lists the staging dir, "~" the user's home.
type FilesListResult struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent,omitempty"`
	Staging string      `json:"staging"`
	Home    string      `json:"home,omitempty"`
	Sep     string      `json:"sep,omitempty"`
	Roots   []string    `json:"roots,omitempty"`
	Trash   bool        `json:"trash,omitempty"` // agent supports FileOpTrash
	Entries []FileEntry `json:"entries"`
}

// File management ops for MethodFilesOp.
const (
	FileOpMkdir  = "mkdir"  // Path
	FileOpRename = "rename" // Path -> To (bare name = same directory)
	FileOpDelete = "delete" // Path; non-empty directories need Confirm
	FileOpTrash  = "trash"  // Path -> moved to the OS recycle bin / Trash
	FileOpZip    = "zip"    // Path (directory) -> staging zip in result Path
)

type FilesOpRequest struct {
	Op      string `json:"op"`
	Path    string `json:"path"`
	To      string `json:"to,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

type FilesOpResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Count int    `json:"count,omitempty"`
}

// Recycle bin management for MethodTrash. IDs are opaque handles from list.
const (
	TrashList    = "list"
	TrashRestore = "restore" // IDs -> back to the original path ("name 2" if taken)
	TrashPurge   = "purge"   // IDs -> deleted for good; needs Confirm
	TrashEmpty   = "empty"   // everything listed; needs Confirm
)

type TrashRequest struct {
	Op      string   `json:"op"`
	IDs     []string `json:"ids,omitempty"`
	Confirm bool     `json:"confirm,omitempty"`
}

type TrashItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Orig       string `json:"orig,omitempty"` // original full path; empty if unknown
	IsDir      bool   `json:"isDir,omitempty"`
	Size       int64  `json:"size,omitempty"`
	DeletedUtc string `json:"deletedUtc,omitempty"`
}

type TrashFailure struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type TrashResult struct {
	Location string         `json:"location,omitempty"` // human-readable trash location
	Items    []TrashItem    `json:"items"`
	Done     int            `json:"done,omitempty"`
	Paths    []string       `json:"paths,omitempty"` // restore targets
	Failed   []TrashFailure `json:"failed,omitempty"`
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
	case MethodHealth, MethodMetrics, MethodProcsList, MethodServicesList:
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
	case MethodFilesOp, MethodTrash:
		return FileOpTimeout
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
