//go:build darwin

package agent

/*
#include <libproc.h>
#include <mach/mach_time.h>
*/
import "C"

import (
	"bytes"
	"errors"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"vartio/internal/entities"
)

// listProcs reads every process through libproc. gopsutil on darwin forks ps
// once per process for the state, and reads the full argument vector with a
// sysctl per pid. One proc_pidinfo plus the executable path is enough for the
// drawer; arguments are not collected.
func listProcs() ([]entities.ProcInfo, error) {
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
	pathBuf := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
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

func machCPUScale() float64 {
	var info C.mach_timebase_info_data_t
	C.mach_timebase_info(&info)
	if info.denom == 0 {
		return 0
	}
	// pti_total_user / pti_total_system are mach ticks. numer/denom converts
	// one tick to nanoseconds.
	return float64(info.numer) / float64(info.denom) / 1e9
}

func darwinPids() ([]int32, error) {
	intSize := int(unsafe.Sizeof(C.int(0)))
	need := int(C.proc_listpids(C.uint32_t(C.PROC_ALL_PIDS), 0, nil, 0))
	if need < 0 {
		return nil, errors.New("proc_listpids")
	}
	slots := need/intSize + 256
	if slots < 1 {
		slots = 1
	}
	buf := make([]C.int, slots)
	n := int(C.proc_listpids(C.uint32_t(C.PROC_ALL_PIDS), 0, unsafe.Pointer(&buf[0]), C.int(slots*intSize)))
	if n < 0 {
		return nil, errors.New("proc_listpids")
	}
	count := n / intSize
	if count > len(buf) {
		count = len(buf)
	}
	out := make([]int32, 0, count)
	for i := 0; i < count; i++ {
		if buf[i] > 0 {
			out = append(out, int32(buf[i]))
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
		name = cChars(info.pbsd.pbi_name[:])
	}
	if name == "" {
		name = cChars(info.pbsd.pbi_comm[:])
	}
	row := entities.ProcInfo{
		PID:     pid,
		Name:    name,
		Status:  darwinStatus(uint32(info.pbsd.pbi_status)),
		MemMB:   float64(info.ptinfo.pti_resident_size) / 1024 / 1024,
		Cmdline: exe,
	}
	return row, taskCPU(info, scale), true
}

func darwinTask(pid int32) (C.struct_proc_taskallinfo, bool) {
	var info C.struct_proc_taskallinfo
	n := C.proc_pidinfo(C.int(pid), C.PROC_PIDTASKALLINFO, 0, unsafe.Pointer(&info), C.int(unsafe.Sizeof(info)))
	return info, int(n) == int(unsafe.Sizeof(info))
}

func taskCPU(info C.struct_proc_taskallinfo, scale float64) float64 {
	return (float64(info.ptinfo.pti_total_user) + float64(info.ptinfo.pti_total_system)) * scale
}

func procExe(pid int32, buf []byte) string {
	if len(buf) == 0 {
		return ""
	}
	n := int(C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf))))
	if n <= 0 {
		return ""
	}
	if n > len(buf) {
		n = len(buf)
	}
	b := buf[:n]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func cChars(s []C.char) string {
	b := make([]byte, 0, len(s))
	for _, c := range s {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
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
