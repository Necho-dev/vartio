package guest

import (
	"encoding/json"
	"slices"
	"time"

	"govmomi-wmks/internal/release"
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
	MethodDisplaySelect  = "display.select"
	MethodDisplayQuality = "display.quality"
	MethodFilesList      = "files.list"
	MethodFilesGet       = "files.get"
	MethodFilesPut       = "files.put"
	MethodFilesOp        = "files.op"
	MethodTrash          = "files.trash"
	MethodUpgrade        = "agent.upgrade"
	MethodTunnelOpen     = "tunnel.open"
	MethodFsWhich        = CapFsWhich
	MethodProcSpawn      = CapProcSpawn
	MethodNetListeners   = CapNetListeners
	MethodFsRead         = CapFsRead
)

const (
	HealthTimeout = 5 * time.Second
	ListTimeout   = 5 * time.Second
	ActionTimeout = 15 * time.Second
	PowerTimeout  = 10 * time.Second
	// UpgradeTimeout covers download, verification and the self-test run.
	UpgradeTimeout  = 3 * time.Minute
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
	// DisplayFPS and DisplayMaxSide are the ceilings across the quality profiles.
	DisplayFPS     = 10
	DisplayMaxSide = 2560
	// MaxTerms caps concurrent terminal sessions (browser tabs) per guest.
	MaxTerms = 8
	// MaxTunnels caps concurrent TCP tunnels per guest; TunnelIdle closes one
	// with no traffic (plugin tunnels; port forwards stay open).
	MaxTunnels  = 64
	TunnelIdle  = 15 * time.Minute
	TunnelChunk = 32 << 10
)

// CapMultiTerm in Hello.Caps means the agent keys PTYs by stream id, so several
// terminals can run at once. Agents without it keep a single PTY.
const CapMultiTerm = "term.multi"

// CapUpgrade means the agent can download, verify and swap in a signed build.
const CapUpgrade = "upgrade"

// CapTunnelTCP means the agent dials TCP targets its local policy allows
// (loopback by default) and relays bytes for tunnel.open.
const CapTunnelTCP = "tunnel.tcp"

// Tunnel byte frames carry the tunnel id in Frame.ID, both directions. Either
// side sends StreamTunnelClose (Data empty) when its end is gone.
const (
	StreamTunnel      = "tun"
	StreamTunnelClose = "tun.close"
)

// Discovery and launch primitives; each is also the RPC method name.
const (
	// CapFsWhich resolves candidate program paths (~, %VAR% and $VAR expanded) and PATH names.
	CapFsWhich = "fs.which"
	// CapProcSpawn starts a detached process in the agent's session and returns its pid.
	CapProcSpawn = "proc.spawn"
	// CapNetListeners lists listening TCP sockets with their owning process.
	CapNetListeners = "net.listeners"
	// CapFsRead reads small files (state and port files), expanded like fs.which.
	CapFsRead = "fs.read"
)

// PrimitiveVersions are the general-purpose primitives this protocol defines,
// keyed by cap name. A field change in a primitive bumps its version.
var PrimitiveVersions = map[string]int{CapTunnelTCP: 1, CapFsWhich: 1, CapProcSpawn: 1, CapNetListeners: 1, CapFsRead: 1}

// PrimitiveMethods maps the RPC-backed primitives to their method.
var PrimitiveMethods = map[string]string{CapFsWhich: MethodFsWhich, CapProcSpawn: MethodProcSpawn, CapNetListeners: MethodNetListeners, CapFsRead: MethodFsRead}

// ReadMax caps fs.read per file; larger files come back truncated.
const ReadMax = 1 << 20

type ReadRequest struct {
	Paths []string `json:"paths"`
	// Limit is bytes per file; 0 means 64KB, at most ReadMax.
	Limit int `json:"limit,omitempty"`
}

type ReadFile struct {
	Query     string `json:"query"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	MtimeUTC  string `json:"mtimeUtc,omitempty"`
	Data      []byte `json:"data"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ReadResult lists the files that exist; missing ones are left out.
type ReadResult struct {
	Files []ReadFile `json:"files"`
}

type WhichRequest struct {
	// Paths are tried as given after expansion; Names are looked up on PATH.
	Paths []string `json:"paths,omitempty"`
	Names []string `json:"names,omitempty"`
}

type WhichHit struct {
	Query    string `json:"query"`
	Path     string `json:"path"`
	Size     int64  `json:"size,omitempty"`
	MtimeUTC string `json:"mtimeUtc,omitempty"`
}

type WhichResult struct {
	Hits []WhichHit `json:"hits"`
}

type SpawnRequest struct {
	Path string            `json:"path"`
	Args []string          `json:"args,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// SpawnResult echoes the expanded command line.
type SpawnResult struct {
	PID  int      `json:"pid"`
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
}

type ListenersRequest struct {
	// Names keeps only processes whose name contains one of these (case-insensitive).
	Names []string `json:"names,omitempty"`
}

type Listener struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	PID  int32  `json:"pid,omitempty"`
	Name string `json:"name,omitempty"`
}

type TunnelOpenRequest struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"` // tcp
	Target string `json:"target"`
}

type TunnelOpenResult struct {
	OK     bool   `json:"ok"`
	Remote string `json:"remote,omitempty"`
}

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
	// Primitives maps each general-purpose primitive the agent serves to its version.
	Primitives map[string]int `json:"primitives,omitempty"`
	OS         string         `json:"os,omitempty"`
	Arch       string         `json:"arch,omitempty"`
	// Runner is how the agent is supervised: service, systemd, launchd or console.
	Runner string `json:"runner,omitempty"`
	// Upgrade reports the outcome of the last upgrade attempt, once, on the next connect.
	Upgrade *UpgradeReport `json:"upgrade,omitempty"`
}

// UpgradeRequest asks the agent to install Version. URL is a hub path; the agent
// resolves it against its control URL and authenticates with its guest token.
type UpgradeRequest struct {
	Version  string           `json:"version"`
	Artifact release.Artifact `json:"artifact"`
	URL      string           `json:"url"`
	Mode     string           `json:"mode"` // push (operator) or auto (agent pull)
}

// UpgradeResult is sent once the build is downloaded, verified and self-tested;
// the agent restarts into it right after replying.
type UpgradeResult struct {
	OK      bool   `json:"ok"`
	From    string `json:"from"`
	To      string `json:"to"`
	Message string `json:"message,omitempty"`
}

// StreamUpgrade frames carry an UpgradeStep for the agent.upgrade call with the same id.
const StreamUpgrade = "upgrade"

// Upgrade stages, in order. The agent reports download/verify/selftest; the hub
// sets the rest from the call result and the next hello's UpgradeReport.
const (
	UpgradeQueued     = "queued"
	UpgradeDownload   = "download"
	UpgradeVerify     = "verify"
	UpgradeSelfTest   = "selftest"
	UpgradeRestart    = "restart"
	UpgradeDone       = "done"
	UpgradeFailed     = "failed"
	UpgradeRolledBack = "rolled_back"
)

type UpgradeStep struct {
	Stage string `json:"stage"`
	Done  int64  `json:"done,omitempty"`
	Total int64  `json:"total,omitempty"`
}

// UpgradeProgress is the hub's view of one device's latest upgrade.
type UpgradeProgress struct {
	ID      string    `json:"id"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Stage   string    `json:"stage"`
	Done    int64     `json:"done,omitempty"`
	Total   int64     `json:"total,omitempty"`
	Message string    `json:"message,omitempty"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
}

type UpgradeReport struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Mode  string `json:"mode,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AgentLatest is the hub's answer to an agent polling for a newer build.
type AgentLatest struct {
	Version  string           `json:"version"`
	Artifact release.Artifact `json:"artifact"`
	URL      string           `json:"url"`
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
	return slices.Contains(h.Caps, c)
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
	TimeMs    int64       `json:"timeMs"`
	Info      SysInfo     `json:"info"`
	CPUPct    float64     `json:"cpuPct"`
	Load      [3]float64  `json:"load"`
	MemTotal  uint64      `json:"memTotal"`
	MemUsed   uint64      `json:"memUsed"`
	SwapTotal uint64      `json:"swapTotal"`
	SwapUsed  uint64      `json:"swapUsed"`
	RootMount string      `json:"rootMount,omitempty"`
	Disks     []DiskUsage `json:"disks"`
	DiskRead  uint64      `json:"diskRead"`
	DiskWrite uint64      `json:"diskWrite"`
	NetRx     uint64      `json:"netRx"`
	NetTx     uint64      `json:"netTx"`
	// Packet, error and drop counters cover the same NICs as NetRx/NetTx (agent >= 1.14.0).
	NetPkts *NetPackets  `json:"netPkts,omitempty"`
	TCP     *TCPStats    `json:"tcp,omitempty"`
	NICs    []NICInfo    `json:"nics"`
	Temps   []TempSensor `json:"temps,omitempty"`
	Procs   int          `json:"procs"`
}

// NetPackets are cumulative counters.
type NetPackets struct {
	Rx      uint64 `json:"rx"`
	Tx      uint64 `json:"tx"`
	ErrIn   uint64 `json:"errIn"`
	ErrOut  uint64 `json:"errOut"`
	DropIn  uint64 `json:"dropIn"`
	DropOut uint64 `json:"dropOut"`
}

// TCPStats counts IPv4+IPv6 TCP sockets by state; segment counters are
// cumulative (retransmission rate = d(Retrans) / d(OutSegs)). OutSegs includes
// retransmissions; on macOS both counters are bytes.
type TCPStats struct {
	Established int `json:"established"`
	TimeWait    int `json:"timeWait"`
	Listen      int `json:"listen"`
	// Socket counts by protocol (agent >= 1.15.0): every TCP state, UDP, and
	// raw/ICMP sockets (Other is 0 on Windows, which has no cheap source).
	Total   int    `json:"total,omitempty"`
	UDP     int    `json:"udp,omitempty"`
	Other   int    `json:"other,omitempty"`
	OutSegs uint64 `json:"outSegs"`
	Retrans uint64 `json:"retrans"`
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
	Online      bool           `json:"online"`
	LastSeenUTC string         `json:"lastSeenUtc,omitempty"`
	Version     string         `json:"version,omitempty"`
	DeviceID    string         `json:"deviceId,omitempty"`
	Device      *DeviceFacts   `json:"device,omitempty"`
	BiosUUID    string         `json:"biosUuid,omitempty"`
	HostName    string         `json:"hostname,omitempty"`
	MACs        []string       `json:"macs,omitempty"`
	RemoteAddr  string         `json:"remoteAddr,omitempty"`
	ConnectedAt string         `json:"connectedAt,omitempty"`
	Caps        []string       `json:"caps,omitempty"`
	Primitives  map[string]int `json:"primitives,omitempty"`
	OS          string         `json:"os,omitempty"`
	Arch        string         `json:"arch,omitempty"`
	Runner      string         `json:"runner,omitempty"`
	// Upgrade is the agent's report of its last upgrade attempt, sent once after it.
	Upgrade *UpgradeReport `json:"upgrade,omitempty"`
}

// DisplayOpenRequest picks the monitor to stream; Screen indexes
// DisplayOpenResult.Screens, and index 0 is always the primary monitor.
type DisplayOpenRequest struct {
	Screen  int    `json:"screen"`
	Quality string `json:"quality,omitempty"`
}

// Display quality profiles. Auto adapts frame rate and picture quality to the
// link and the machine; smooth favours frame rate, hd favours picture quality.
const (
	DisplayAuto   = "auto"
	DisplaySmooth = "smooth"
	DisplayHD     = "hd"
)

type DisplayOpenResult struct {
	OK      bool `json:"ok"`
	FPS     int  `json:"fps"`
	MaxSide int  `json:"maxSide"`
	Width   int  `json:"width,omitempty"`
	Height  int  `json:"height,omitempty"`
	// Screens is empty for agents that can only capture the primary monitor.
	Screens []Screen `json:"screens,omitempty"`
	Screen  int      `json:"screen"`
	// Quality is empty for agents without quality profiles.
	Quality string `json:"quality,omitempty"`
}

// Screen is one monitor in desktop coordinates (pixels, origin at the
// primary monitor's top-left corner).
type Screen struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Primary bool   `json:"primary,omitempty"`
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
	case MethodHealth, MethodMetrics, MethodProcsList, MethodServicesList, MethodFsWhich, MethodFsRead:
		return ListTimeout
	case MethodProcsAction, MethodServicesAction, MethodProcSpawn, MethodNetListeners:
		return ActionTimeout
	case MethodPower:
		return PowerTimeout
	case MethodTunnelOpen, MethodTermOpen, MethodTermClose, MethodTermResize, MethodDisplayOpen, MethodDisplayClose, MethodDisplaySelect, MethodDisplayQuality:
		return TermOpenTimeout
	case MethodFilesList:
		return ListTimeout
	case MethodFilesGet, MethodFilesPut:
		return FileChunkWait
	case MethodFilesOp, MethodTrash:
		return FileOpTimeout
	case MethodUpgrade:
		return UpgradeTimeout
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
