//go:build linux

package agent

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/mem"
)

func memoryDetail(vm *mem.VirtualMemoryStat) (used *uint64, compressed, cached uint64, ok bool) {
	if vm == nil || vm.Total == 0 {
		return nil, 0, 0, false
	}
	// gopsutil folds SReclaimable into Cached. Shared is Shmem, which is not
	// freely reclaimable. Zswap is the compressed store; zram shows up as swap.
	var cache uint64
	if sum := vm.Buffers + vm.Cached; sum > vm.Shared {
		cache = sum - vm.Shared
	}
	zswap := meminfoKB("Zswap") * 1024
	_, comp, clamped := clampMem(vm.Total, vm.Available, zswap, cache)
	return nil, comp, clamped, true
}

func meminfoKB(key string) uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	prefix := key + ":"
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}
