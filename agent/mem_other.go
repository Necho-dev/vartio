//go:build !darwin && !linux && !windows

package agent

import "github.com/shirou/gopsutil/v4/mem"

func memoryDetail(*mem.VirtualMemoryStat) (used *uint64, compressed, cached uint64, ok bool) {
	return nil, 0, 0, false
}
