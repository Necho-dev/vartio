//go:build windows

package agent

import (
	"context"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
)

// Windows has no load average. Like Linux we count runnable threads: the
// running ones (CPU utilisation × cores) plus the Processor Queue Length that
// gopsutil samples, each smoothed with the kernel's 5s exponential decay.
// Both terms decay identically, so their averages simply add up.
const loadTick = 5 * time.Second

var winLoad struct {
	once sync.Once
	mu   sync.Mutex
	avg  [3]float64
}

func loadAvg(context.Context) ([3]float64, bool) {
	winLoad.once.Do(func() { go sampleRunning() })
	// gopsutil binds its sampler to the first ctx it sees; ours is a per-poll timeout.
	q, err := load.AvgWithContext(context.Background())
	winLoad.mu.Lock()
	out := winLoad.avg
	winLoad.mu.Unlock()
	if err == nil {
		out[0] += q.Load1
		out[1] += q.Load5
		out[2] += q.Load15
	}
	return out, true
}

func sampleRunning() {
	var decay [3]float64
	for i, m := range []float64{1, 5, 15} {
		decay[i] = math.Exp(-loadTick.Seconds() / (m * 60))
	}
	cores := float64(runtime.NumCPU())
	prevBusy, prevTotal, ok := cpuTimes()
	seeded := false
	for range time.Tick(loadTick) {
		busy, total, ok2 := cpuTimes()
		if !ok || !ok2 || total <= prevTotal {
			prevBusy, prevTotal, ok = busy, total, ok2
			continue
		}
		running := (busy - prevBusy) / (total - prevTotal) * cores
		prevBusy, prevTotal = busy, total
		winLoad.mu.Lock()
		for i := range winLoad.avg {
			if !seeded {
				// Start from the current level rather than ramping up from 0 for 15 minutes.
				winLoad.avg[i] = running
			} else {
				winLoad.avg[i] = winLoad.avg[i]*decay[i] + running*(1-decay[i])
			}
		}
		winLoad.mu.Unlock()
		seeded = true
	}
}

func cpuTimes() (busy, total float64, ok bool) {
	t, err := cpu.Times(false)
	if err != nil || len(t) == 0 {
		return 0, 0, false
	}
	c := t[0]
	total = c.User + c.System + c.Idle + c.Nice + c.Iowait + c.Irq + c.Softirq + c.Steal
	return total - c.Idle - c.Iowait, total, true
}
