//go:build !windows

package agent

import (
	"context"

	"github.com/shirou/gopsutil/v4/load"
)

func loadAvg(ctx context.Context) ([3]float64, bool) {
	l, err := load.AvgWithContext(ctx)
	if err != nil {
		return [3]float64{}, false
	}
	return [3]float64{l.Load1, l.Load5, l.Load15}, true
}
