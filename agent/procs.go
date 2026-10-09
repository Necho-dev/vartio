package main

import (
	"fmt"
	"strings"

	"github.com/shirou/gopsutil/v4/process"

	"govmomi-wmks/internal/guest"
)

func listProcs() ([]guest.ProcInfo, error) {
	ps, err := process.Processes()
	if err != nil {
		return nil, err
	}
	out := make([]guest.ProcInfo, 0, len(ps))
	for _, p := range ps {
		name, _ := p.Name()
		st, _ := p.Status()
		cpuPct, _ := p.CPUPercent()
		rss, _ := p.MemoryInfo()
		cmd, _ := p.Cmdline()
		info := guest.ProcInfo{PID: p.Pid, Name: name, CPUPct: cpuPct, Cmdline: cmd}
		if len(st) > 0 {
			info.Status = st[0]
		}
		if rss != nil {
			info.MemMB = float64(rss.RSS) / 1024 / 1024
		}
		out = append(out, info)
	}
	return out, nil
}

func procAlive(name string) (bool, string) {
	ps, err := process.Processes()
	if err != nil {
		return false, err.Error()
	}
	want := strings.ToLower(name)
	for _, p := range ps {
		n, _ := p.Name()
		if strings.EqualFold(n, name) || strings.Contains(strings.ToLower(n), want) {
			return true, fmt.Sprintf("pid=%d", p.Pid)
		}
	}
	return false, "not running"
}

func procAction(req guest.ActionRequest) (guest.ActionResult, *guest.FrameError) {
	ps, err := process.Processes()
	if err != nil {
		return guest.ActionResult{}, asRPC(err)
	}
	want := strings.ToLower(req.Name)
	var hits []*process.Process
	for _, p := range ps {
		n, _ := p.Name()
		if strings.EqualFold(n, req.Name) || strings.Contains(strings.ToLower(n), want) {
			hits = append(hits, p)
		}
	}
	switch req.Action {
	case "start":
		return guest.ActionResult{}, &guest.FrameError{Code: "not_supported", Message: "start a process by path via exec, not procs.action"}
	case "stop", "kill":
		if len(hits) == 0 {
			return guest.ActionResult{OK: false, Name: req.Name, Action: req.Action, Message: "not found"}, nil
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
			return guest.ActionResult{}, asRPC(last)
		}
		return guest.ActionResult{OK: true, Name: req.Name, Action: req.Action}, nil
	default:
		return guest.ActionResult{}, bad("action must be start, stop, or kill")
	}
}
