package agent

import (
	"context"
	"sync"

	"github.com/shirou/gopsutil/v4/cpu"

	"vartio/internal/entities"
)

// cpuState keeps the previous absolute counters so one interval yields a percentage.
var cpuState struct {
	mu    sync.Mutex
	ok    bool
	all   cpu.TimesStat
	perOK bool
	per   []cpu.TimesStat
}

// sampleCPU reports total, user, system, and iowait as percentages of one interval,
// plus one entry per logical processor. The split and the core list are empty on
// the first call. Total falls back to gopsutil until that second sample exists.
func sampleCPU(ctx context.Context) (pct float64, user, system, wait *float64, cores []entities.CPUCore) {
	all, err := cpu.TimesWithContext(ctx, false)
	per, perErr := cpu.TimesWithContext(ctx, true)
	cpuState.mu.Lock()
	defer cpuState.mu.Unlock()
	if err == nil && len(all) > 0 && cpuState.ok {
		if total, u, s, w, ok := cpuParts(cpuState.all, all[0]); ok {
			pct = total
			user, system, wait = &u, &s, &w
		}
	}
	if perErr == nil && cpuState.perOK && len(per) > 0 && len(per) == len(cpuState.per) {
		kinds := coreKinds(len(per))
		cores = make([]entities.CPUCore, len(per))
		for i := range per {
			cores[i].Pct = cpuBusy(cpuState.per[i], per[i])
			if i < len(kinds) {
				cores[i].Kind = kinds[i]
			}
		}
	}
	if err == nil && len(all) > 0 {
		cpuState.all = all[0]
		cpuState.ok = true
	}
	if perErr == nil && len(per) > 0 {
		cpuState.per = per
		cpuState.perOK = true
	}
	if user == nil {
		if p, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(p) > 0 {
			pct = p[0]
		}
	}
	return pct, user, system, wait, cores
}

// cpuParts splits one interval. User includes nice. System includes irq and steal.
// Wait is iowait, which is zero on macOS and Windows. Total busy matches gopsutil.
func cpuParts(prev, cur cpu.TimesStat) (total, user, system, wait float64, ok bool) {
	du := (cur.User + cur.Nice) - (prev.User + prev.Nice)
	ds := (cur.System + cur.Irq + cur.Softirq + cur.Steal) - (prev.System + prev.Irq + prev.Softirq + prev.Steal)
	dw := cur.Iowait - prev.Iowait
	di := cur.Idle - prev.Idle
	all := du + ds + dw + di
	if all <= 0 || du < 0 || ds < 0 || dw < 0 || di < 0 {
		return 0, 0, 0, 0, false
	}
	user = du / all * 100
	system = ds / all * 100
	wait = dw / all * 100
	total = (du + ds + dw) / all * 100
	return total, user, system, wait, true
}

func cpuBusy(prev, cur cpu.TimesStat) float64 {
	total, _, _, _, ok := cpuParts(prev, cur)
	if !ok {
		return 0
	}
	return total
}

// perfKinds labels the first perf logical processors as performance and the rest
// as efficiency. A count that does not cover n leaves every core unlabeled.
func perfKinds(n, perf, eff int) []string {
	if n <= 0 || perf <= 0 || eff <= 0 || perf+eff != n {
		return nil
	}
	out := make([]string, n)
	for i := 0; i < perf; i++ {
		out[i] = "p"
	}
	for i := perf; i < n; i++ {
		out[i] = "e"
	}
	return out
}

// capacityKinds labels cores that sit below the highest cpu_capacity as efficiency.
func capacityKinds(caps []int) []string {
	if len(caps) == 0 {
		return nil
	}
	max := 0
	for _, c := range caps {
		if c > max {
			max = c
		}
	}
	if max == 0 {
		return nil
	}
	varied := false
	for _, c := range caps {
		if c > 0 && c < max {
			varied = true
			break
		}
	}
	if !varied {
		return nil
	}
	out := make([]string, len(caps))
	for i, c := range caps {
		if c >= max {
			out[i] = "p"
		} else {
			out[i] = "e"
		}
	}
	return out
}
