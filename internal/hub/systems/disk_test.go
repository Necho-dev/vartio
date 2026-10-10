package monitor

import (
	"testing"
	"time"

	"govmomi-wmks/internal/guest"
)

func TestDeriveDiskSumsPartitions(t *testing.T) {
	m := &Monitor{prev: map[string]counters{}}
	smp := m.derive("id", guest.Metrics{
		RootMount: "/oldroot",
		Disks: []guest.DiskUsage{
			{Mount: "/oldroot", FS: "erofs", Total: 100, Used: 100},
			{Mount: "/var/lib", FS: "ext4", Total: 1000, Used: 10},
		},
	}, time.Second)
	if smp.DiskUsed != 110 || smp.DiskTotal != 1100 {
		t.Fatalf("sum = %d/%d", smp.DiskUsed, smp.DiskTotal)
	}
	if smp.DiskPct < 9.9 || smp.DiskPct > 10.1 {
		t.Fatalf("pct = %v", smp.DiskPct)
	}
	apfs := guest.DiskUsage{FS: "apfs", Total: 500, Used: 400}
	sys, data := apfs, apfs
	sys.Mount, data.Mount = "/", "/System/Volumes/Data"
	ext := guest.DiskUsage{Mount: "/Volumes/X", FS: "apfs", Total: 100, Used: 1}
	shared := m.derive("mac", guest.Metrics{
		RootMount: "/System/Volumes/Data",
		Disks:     []guest.DiskUsage{sys, data, ext},
	}, time.Second)
	if shared.DiskUsed != 401 || shared.DiskTotal != 600 {
		t.Fatalf("shared pool counted twice: %d/%d", shared.DiskUsed, shared.DiskTotal)
	}
}
