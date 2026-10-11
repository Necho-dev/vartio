//go:build windows

package agent

import (
	"net"
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

// nonUnicastPkts returns broadcast + multicast packet counts, which gopsutil
// leaves out of PacketsRecv/Sent on Windows although its byte counts include them.
func nonUnicastPkts(name string) (rx, tx uint64) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return 0, 0
	}
	row := windows.MibIfRow2{InterfaceIndex: uint32(ifi.Index)}
	if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) != nil {
		return 0, 0
	}
	return row.InNUcastPkts, row.OutNUcastPkts
}
