//go:build darwin

package agent

import (
	"context"
	"time"

	"vartio/internal/entities"
)

func platformGPUs(ctx context.Context) []entities.GPUInfo {
	out := appleGPUs(ctx)
	return append(out, nvidiaGPUs(ctx)...)
}

func appleGPUs(ctx context.Context) []entities.GPUInfo {
	// Four IOKit snapshots across about a second. The stored point is the peak
	// of device, renderer and tiler. The counters are instantaneous and often
	// zero between frames; the ioreg command never refreshes them.
	var best []entities.GPUInfo
	for i := 0; i < 4; i++ {
		if ctx.Err() != nil {
			break
		}
		best = peakGPUs(best, readAppleGPU(ctx))
		if i == 0 && len(best) == 0 {
			return nil
		}
		if i == 3 {
			break
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return best
		case <-timer.C:
		}
	}
	return best
}

func readAppleGPU(ctx context.Context) []entities.GPUInfo {
	if ctx.Err() != nil {
		return nil
	}
	return appleGPUFromIOKit()
}
