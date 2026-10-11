package agent

import (
	"testing"

	"vartio/internal/entities"
)

func TestPickRootSkipsInitrd(t *testing.T) {
	disks := []entities.DiskUsage{
		{Mount: "/oldroot", FS: "erofs", Total: 644 << 20, Used: 644 << 20},
		{Mount: "/var/lib", FS: "ext4", Total: 452 << 30, Used: 3 << 30},
	}
	if got := pickRoot(disks); got != "/var/lib" {
		t.Fatalf("pickRoot = %s", got)
	}
}

func TestPickRootKeepsWritableRoot(t *testing.T) {
	disks := []entities.DiskUsage{
		{Mount: "/", FS: "ext4", Total: 100, Used: 40},
		{Mount: "/data", FS: "ext4", Total: 900, Used: 10},
	}
	if got := pickRoot(disks); got != "/" {
		t.Fatalf("pickRoot = %s", got)
	}
}
