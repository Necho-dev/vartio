package guest

import "testing"

func TestDedupePoolsFoldsSystemVolume(t *testing.T) {
	sys := DiskUsage{Mount: "/", FS: "apfs", Total: 500, Used: 12}
	data := DiskUsage{Mount: "/System/Volumes/Data", FS: "apfs", Total: 500, Used: 400}
	ext := DiskUsage{Mount: "/Volumes/X", FS: "apfs", Total: 500, Used: 1}
	got := DedupePools([]DiskUsage{sys, data, ext}, "/System/Volumes/Data")
	if len(got) != 2 || got[0].Mount != "/System/Volumes/Data" || got[1].Mount != "/Volumes/X" {
		t.Fatalf("used mismatch still split the system volume: %+v", got)
	}
	if used, total := DiskSum(got); used != 401 || total != 1000 {
		t.Fatalf("sum = %d/%d", used, total)
	}
}

func TestDiskSum(t *testing.T) {
	old := DiskUsage{Mount: "/oldroot", FS: "erofs", Total: 100, Used: 100}
	data := DiskUsage{Mount: "/var/lib", FS: "ext4", Total: 1000, Used: 10}
	used, total := DiskSum([]DiskUsage{old, data, {Mount: "/empty", FS: "ext4"}})
	if used != 110 || total != 1100 {
		t.Fatalf("sum = %d/%d", used, total)
	}
}

func TestCapacityMount(t *testing.T) {
	old := DiskUsage{Mount: "/oldroot", FS: "erofs", Total: 644 << 20, Used: 644 << 20}
	data := DiskUsage{Mount: "/var/lib", FS: "ext4", Total: 452 << 30, Used: 3 << 30}
	if got := CapacityMount([]DiskUsage{old, data}, "/oldroot"); got != "/var/lib" {
		t.Fatalf("initrd preferred = %s", got)
	}
	root := DiskUsage{Mount: "/", FS: "ext4", Total: 100, Used: 40}
	extra := DiskUsage{Mount: "/data", FS: "ext4", Total: 900, Used: 10}
	if got := CapacityMount([]DiskUsage{root, extra}, "/"); got != "/" {
		t.Fatalf("real root = %s", got)
	}
	if got := CapacityMount([]DiskUsage{old}, "/oldroot"); got != "/oldroot" {
		t.Fatalf("image only = %s", got)
	}
	if got := CapacityMount(nil, "/"); got != "" {
		t.Fatalf("empty = %q", got)
	}
	short := DiskUsage{Mount: "/var", FS: "ext4", Total: 10, Used: 1}
	long := DiskUsage{Mount: "/var/lib/extra", FS: "ext4", Total: 10, Used: 1}
	if got := CapacityMount([]DiskUsage{long, short}, ""); got != "/var" {
		t.Fatalf("tie = %s", got)
	}
}
