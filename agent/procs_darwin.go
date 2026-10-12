//go:build darwin

package agent

import (
	"bytes"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"vartio/internal/entities"
)

// listProcs reads every process through libproc. The calls go through purego,
// so this stays in the CGO_ENABLED=0 build. gopsutil on darwin forks ps once
// per process for the state and reads the full argument vector per pid.
// Arguments are not collected; Cmdline is the executable path.
func listProcs() ([]entities.ProcInfo, error) {
	if err := libprocOpen(); err != nil {
		return nil, err
	}
	scale := machCPUScale()
	before := map[int32]float64{}
	pids, err := darwinPids()
	if err != nil {
		return nil, err
	}
	for _, pid := range pids {
		if cpu, ok := darwinCPU(pid, scale); ok {
			before[pid] = cpu
		}
	}
	t0 := time.Now()
	time.Sleep(procSampleGap)
	dt := time.Since(t0).Seconds() * float64(runtime.NumCPU())

	pids, err = darwinPids()
	if err != nil {
		return nil, err
	}
	pathBuf := make([]byte, procPathMax)
	out := make([]entities.ProcInfo, 0, len(pids))
	for _, pid := range pids {
		info, cpu, ok := darwinProc(pid, scale, pathBuf)
		if !ok {
			continue
		}
		if b, had := before[pid]; had && cpu >= b && dt > 0 {
			info.CPUPct = (cpu - b) / dt * 100
		}
		out = append(out, info)
	}
	return out, nil
}

const (
	procAllPids    = 1
	procTaskAll    = 2
	procPathMax    = 4096 // PROC_PIDPATHINFO_MAXSIZE
	procTaskAllLen = 232  // sizeof(struct proc_taskallinfo) on this OS
)

type procBSDInfo struct {
	flags, status, xstatus, pid, ppid  uint32
	uid, gid, ruid, rgid, svuid, svgid uint32
	rfu                                uint32
	comm                               [16]byte
	name                               [32]byte
	nfiles, pgid, pjobc, tdev, tpgid   uint32
	nice                               int32
	startSec, startUsec                uint64
}

type procTaskInfo struct {
	virtual, resident, user, system, threadsUser, threadsSystem uint64
	policy, faults, pageins, cow                                int32
	msgSent, msgRecv, sysMach, sysUnix                          int32
	csw, threads, running, priority                             int32
}

type procTaskAllInfo struct {
	bsd  procBSDInfo
	task procTaskInfo
}

func init() {
	if unsafe.Sizeof(procTaskAllInfo{}) != procTaskAllLen || unsafe.Sizeof(procBSDInfo{}) != 136 {
		panic("proc_taskallinfo layout does not match libproc")
	}
}

var (
	libprocOnce             sync.Once
	libprocErr              error
	symListpids, symPidinfo uintptr
	symPidpath, symTimebase uintptr
)

func libprocOpen() error {
	libprocOnce.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY)
		if err != nil {
			libprocErr = err
			return
		}
		for _, bind := range []struct {
			dst  *uintptr
			name string
		}{
			{&symListpids, "proc_listpids"},
			{&symPidinfo, "proc_pidinfo"},
			{&symPidpath, "proc_pidpath"},
			{&symTimebase, "mach_timebase_info"},
		} {
			*bind.dst, err = purego.Dlsym(h, bind.name)
			if err != nil {
				libprocErr = err
				return
			}
		}
	})
	return libprocErr
}

func machCPUScale() float64 {
	var info struct{ numer, denom uint32 }
	purego.SyscallN(symTimebase, uintptr(unsafe.Pointer(&info)))
	if info.denom == 0 {
		return 0
	}
	// pti_total_user / pti_total_system are mach ticks. numer/denom converts
	// one tick to nanoseconds.
	return float64(info.numer) / float64(info.denom) / 1e9
}

func darwinPids() ([]int32, error) {
	intSize := int(unsafe.Sizeof(int32(0)))
	need, _, _ := purego.SyscallN(symListpids, procAllPids, 0, 0, 0)
	if int32(need) < 0 {
		return nil, errors.New("proc_listpids")
	}
	slots := int(need)/intSize + 256
	if slots < 1 {
		slots = 1
	}
	buf := make([]int32, slots)
	n, _, _ := purego.SyscallN(symListpids, procAllPids, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(slots*intSize))
	if int32(n) < 0 {
		return nil, errors.New("proc_listpids")
	}
	count := int(n) / intSize
	if count > len(buf) {
		count = len(buf)
	}
	out := make([]int32, 0, count)
	for i := 0; i < count; i++ {
		if buf[i] > 0 {
			out = append(out, buf[i])
		}
	}
	return out, nil
}

func darwinCPU(pid int32, scale float64) (float64, bool) {
	info, ok := darwinTask(pid)
	if !ok {
		return 0, false
	}
	return taskCPU(info, scale), true
}

func darwinProc(pid int32, scale float64, pathBuf []byte) (entities.ProcInfo, float64, bool) {
	info, ok := darwinTask(pid)
	if !ok {
		return entities.ProcInfo{}, 0, false
	}
	exe := procExe(pid, pathBuf)
	name := filepath.Base(exe)
	if name == "." || name == string(filepath.Separator) {
		name = ""
	}
	if name == "" {
		name = cBytes(info.bsd.name[:])
	}
	if name == "" {
		name = cBytes(info.bsd.comm[:])
	}
	row := entities.ProcInfo{
		PID:     pid,
		Name:    name,
		Status:  darwinStatus(info.bsd.status),
		MemMB:   float64(info.task.resident) / 1024 / 1024,
		Cmdline: exe,
	}
	return row, taskCPU(info, scale), true
}

func darwinTask(pid int32) (procTaskAllInfo, bool) {
	var info procTaskAllInfo
	n, _, _ := purego.SyscallN(symPidinfo, uintptr(pid), procTaskAll, 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	return info, int(int32(n)) == int(unsafe.Sizeof(info))
}

func taskCPU(info procTaskAllInfo, scale float64) float64 {
	return (float64(info.task.user) + float64(info.task.system)) * scale
}

func procExe(pid int32, buf []byte) string {
	if len(buf) == 0 {
		return ""
	}
	n, _, _ := purego.SyscallN(symPidpath, uintptr(pid), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if int32(n) <= 0 {
		return ""
	}
	got := int(int32(n))
	if got > len(buf) {
		got = len(buf)
	}
	b := buf[:got]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func cBytes(s []byte) string {
	if i := bytes.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return string(s)
}

// pbi_status is the BSD p_stat: SIDL, SRUN, SSLEEP, SSTOP, SZOMB.
func darwinStatus(st uint32) string {
	switch st {
	case 1:
		return "idle"
	case 2:
		return "running"
	case 3:
		return "sleep"
	case 4:
		return "stop"
	case 5:
		return "zombie"
	default:
		return ""
	}
}
