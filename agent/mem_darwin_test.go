//go:build darwin

package agent

import (
	"testing"
	"unsafe"

	"github.com/shirou/gopsutil/v4/mem"
)

func TestVMStat64Layout(t *testing.T) {
	if unsafe.Sizeof(vmStat64{}) != 152 {
		t.Fatalf("vm_statistics64 size %d, want 152", unsafe.Sizeof(vmStat64{}))
	}
}

func TestDarwinMemoryDetail(t *testing.T) {
	vm, err := mem.VirtualMemory()
	if err != nil {
		t.Fatal(err)
	}
	used, comp, cache, ok := memoryDetail(vm)
	if !ok || used == nil {
		t.Fatal("memory detail missing")
	}
	if *used > vm.Total || comp > *used || cache > vm.Total-*used {
		t.Fatalf("total=%d used=%d compressed=%d cached=%d", vm.Total, *used, comp, cache)
	}
	t.Logf("used=%d compressed=%d cached=%d total=%d", *used, comp, cache, vm.Total)
}
