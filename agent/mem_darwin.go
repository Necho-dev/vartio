//go:build darwin

package agent

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sys/unix"
)

// vmStat64 matches vm_statistics64. HOST_VM_INFO64_COUNT is 38 integer_t slots.
type vmStat64 struct {
	free, active, inactive, wire                    uint32
	zeroFill, reactivations, pageins, pageouts      uint64
	faults, cowFaults, lookups, hits, purges        uint64
	purgeable, speculative                          uint32
	decompressions, compressions, swapins, swapouts uint64
	compressor, throttled, external, internal       uint32
	uncompressed                                    uint64
}

const hostVMInfo64 = 4

var (
	vmOnce      sync.Once
	vmErr       error
	symHostStat uintptr
	symMachHost uintptr
)

func vmInfo64() (vmStat64, uint64, error) {
	vmOnce.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY)
		if err != nil {
			vmErr = err
			return
		}
		for _, bind := range []struct {
			dst  *uintptr
			name string
		}{
			{&symHostStat, "host_statistics64"},
			{&symMachHost, "mach_host_self"},
		} {
			*bind.dst, err = purego.Dlsym(h, bind.name)
			if err != nil {
				vmErr = err
				return
			}
		}
	})
	if vmErr != nil {
		return vmStat64{}, 0, vmErr
	}
	host, _, _ := purego.SyscallN(symMachHost)
	var st vmStat64
	count := uint32(unsafe.Sizeof(st) / 4)
	status, _, _ := purego.SyscallN(symHostStat, host, hostVMInfo64, uintptr(unsafe.Pointer(&st)), uintptr(unsafe.Pointer(&count)))
	if status != 0 {
		return vmStat64{}, 0, fmt.Errorf("host_statistics64 status %d", status)
	}
	return st, uint64(unix.Getpagesize()), nil
}

func memoryDetail(vm *mem.VirtualMemoryStat) (used *uint64, compressed, cached uint64, ok bool) {
	if vm == nil || vm.Total == 0 {
		return nil, 0, 0, false
	}
	st, page, err := vmInfo64()
	if err != nil || page == 0 {
		return nil, 0, 0, false
	}
	pages := func(n uint32) uint64 { return uint64(n) * page }
	app := pages(st.internal)
	purgeable := pages(st.purgeable)
	if purgeable < app {
		app -= purgeable
	} else {
		app = 0
	}
	wired := pages(st.wire)
	comp := pages(st.compressor)
	cache := pages(st.external) + purgeable
	sum := app + wired + comp
	if sum > vm.Total {
		sum = vm.Total
	}
	u, c, k := clampMem(vm.Total, vm.Total-sum, comp, cache)
	return &u, c, k, true
}
