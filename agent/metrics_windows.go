//go:build windows

package main

import (
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
	"golang.org/x/sys/windows"
)

// skipMountOS keeps local fixed and removable drives; network shares and optical drives are skipped.
func skipMountOS(p disk.PartitionStat) bool {
	root := strings.TrimSuffix(p.Mountpoint, `\`) + `\`
	ptr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return false
	}
	switch windows.GetDriveType(ptr) {
	case windows.DRIVE_REMOTE, windows.DRIVE_CDROM, windows.DRIVE_RAMDISK:
		return true
	}
	return false
}
