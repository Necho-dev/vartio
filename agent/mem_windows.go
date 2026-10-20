//go:build windows

package agent

import (
	"strings"
	"unsafe"

	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sys/windows"
)

var procGetPerformanceInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetPerformanceInfo")

type performanceInformation struct {
	cb                uint32
	commitTotal       uint64
	commitLimit       uint64
	commitPeak        uint64
	physicalTotal     uint64
	physicalAvailable uint64
	systemCache       uint64
	kernelTotal       uint64
	kernelPaged       uint64
	kernelNonpaged    uint64
	pageSize          uint64
	handleCount       uint32
	processCount      uint32
	threadCount       uint32
}

func memoryDetail(vm *mem.VirtualMemoryStat) (used *uint64, compressed, cached uint64, ok bool) {
	if vm == nil || vm.Total == 0 {
		return nil, 0, 0, false
	}
	cache := systemCacheBytes()
	comp := compressionWorkingSet()
	_, c, k := clampMem(vm.Total, vm.Available, comp, cache)
	return nil, c, k, true
}

func systemCacheBytes() uint64 {
	var info performanceInformation
	info.cb = uint32(unsafe.Sizeof(info))
	r, _, _ := procGetPerformanceInfo.Call(uintptr(unsafe.Pointer(&info)), uintptr(info.cb))
	if r == 0 || info.pageSize == 0 {
		return 0
	}
	return info.systemCache * info.pageSize
}

// compressionWorkingSet is the working set of "Memory Compression", the store
// Task Manager shows in parentheses after 使用中. That process rejects
// OpenProcess from a non-elevated agent, so the size comes from the system
// process list instead.
func compressionWorkingSet() uint64 {
	var buf []byte
	var n uint32
	for sz := uint32(1 << 20); ; sz = n {
		buf = make([]byte, sz)
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n)
		if err == nil {
			break
		}
		if err != windows.STATUS_INFO_LENGTH_MISMATCH || n <= uint32(len(buf)) {
			return 0
		}
	}
	for offset := uintptr(0); offset < uintptr(len(buf)); {
		p := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[offset]))
		if p.ImageName.Buffer != nil && p.ImageName.Length > 0 && strings.EqualFold(p.ImageName.String(), "Memory Compression") {
			return uint64(p.WorkingSetSize)
		}
		if p.NextEntryOffset == 0 {
			return 0
		}
		offset += uintptr(p.NextEntryOffset)
	}
	return 0
}
