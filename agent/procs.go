package agent

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"vartio/internal/entities"
)

const procSampleGap = 700 * time.Millisecond

func procName(p *process.Process, names map[int32]string) string {
	if n, err := p.Name(); err == nil && n != "" {
		return n
	}
	return names[p.Pid]
}

func cpuSeconds(p *process.Process) (float64, bool) {
	t, err := p.Times()
	if err != nil {
		return 0, false
	}
	return t.User + t.System, true
}

// listProcsGopsutil samples CPU time twice so cpuPct is the current share of
// all cores, not the lifetime average gopsutil reports. macOS does not use
// this path: Status shells out to ps once per process, and Cmdline is a
// sysctl per process.
func listProcsGopsutil() ([]entities.ProcInfo, error) {
	ps, err := process.Processes()
	if err != nil {
		return nil, err
	}
	names := procNames()
	before := make(map[int32]float64, len(ps))
	for _, p := range ps {
		if s, ok := cpuSeconds(p); ok {
			before[p.Pid] = s
		}
	}
	t0 := time.Now()
	time.Sleep(procSampleGap)
	dt := time.Since(t0).Seconds() * float64(runtime.NumCPU())

	out := make([]entities.ProcInfo, 0, len(ps))
	for _, p := range ps {
		info := entities.ProcInfo{PID: p.Pid, Name: procName(p, names)}
		if s, ok := cpuSeconds(p); ok {
			if b, had := before[p.Pid]; had && s >= b && dt > 0 {
				info.CPUPct = (s - b) / dt * 100
			}
		}
		if st, _ := p.Status(); len(st) > 0 {
			info.Status = st[0]
		}
		if rss, err := p.MemoryInfo(); err == nil && rss != nil {
			info.MemMB = float64(rss.RSS) / 1024 / 1024
		}
		info.Cmdline, _ = p.Cmdline()
		out = append(out, info)
	}
	return out, nil
}

func matchProcs(name string) ([]*process.Process, error) {
	ps, err := process.Processes()
	if err != nil {
		return nil, err
	}
	names := procNames()
	want := strings.ToLower(name)
	var hits []*process.Process
	for _, p := range ps {
		n := procName(p, names)
		if n == "" {
			continue
		}
		if strings.EqualFold(n, name) || strings.Contains(strings.ToLower(n), want) {
			hits = append(hits, p)
		}
	}
	return hits, nil
}

func procAlive(name string) (bool, string) {
	hits, err := matchProcs(name)
	if err != nil {
		return false, err.Error()
	}
	if len(hits) == 0 {
		return false, "not running"
	}
	return true, fmt.Sprintf("pid=%d", hits[0].Pid)
}

func procAction(req entities.ActionRequest) (entities.ActionResult, *entities.FrameError) {
	switch req.Action {
	case "start":
		return entities.ActionResult{}, &entities.FrameError{Code: "not_supported", Message: "start a process by path via exec, not procs.action"}
	case "stop", "kill":
	default:
		return entities.ActionResult{}, bad("action must be start, stop, or kill")
	}
	hits, err := matchProcs(req.Name)
	if err != nil {
		return entities.ActionResult{}, asRPC(err)
	}
	if len(hits) == 0 {
		return entities.ActionResult{OK: false, Name: req.Name, Action: req.Action, Message: "not found"}, nil
	}
	var last error
	for _, p := range hits {
		if req.Action == "kill" {
			last = p.Kill()
		} else {
			last = p.Terminate()
		}
	}
	if last != nil {
		return entities.ActionResult{}, asRPC(last)
	}
	return entities.ActionResult{OK: true, Name: req.Name, Action: req.Action}, nil
}
