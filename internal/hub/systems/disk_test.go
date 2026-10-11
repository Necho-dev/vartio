package systems

import (
	"testing"
	"time"
	"vartio/internal/entities"
)

func TestDeriveDiskSumsPartitions(t *testing.T) {
	m := &Monitor{prev: map[string]counters{}}
	smp := m.derive("id", entities.Metrics{
		RootMount: "/oldroot",
		Disks: []entities.DiskUsage{
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
	apfs := entities.DiskUsage{FS: "apfs", Total: 500, Used: 400}
	sys, data := apfs, apfs
	sys.Mount, data.Mount = "/", "/System/Volumes/Data"
	ext := entities.DiskUsage{Mount: "/Volumes/X", FS: "apfs", Total: 100, Used: 1}
	shared := m.derive("mac", entities.Metrics{
		RootMount: "/System/Volumes/Data",
		Disks:     []entities.DiskUsage{sys, data, ext},
	}, time.Second)
	if shared.DiskUsed != 401 || shared.DiskTotal != 600 {
		t.Fatalf("shared pool counted twice: %d/%d", shared.DiskUsed, shared.DiskTotal)
	}
}
