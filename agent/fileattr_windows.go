//go:build windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func hiddenAttr(st os.FileInfo) bool {
	d, ok := st.Sys().(*syscall.Win32FileAttributeData)
	return ok && d.FileAttributes&(windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_SYSTEM) != 0
}

// fileRoots lists drive letters that have a root directory (skips empty slots).
func fileRoots() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []string
	for i := range 26 {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(p) == windows.DRIVE_NO_ROOT_DIR {
			continue
		}
		out = append(out, root)
	}
	return out
}
