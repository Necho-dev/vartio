//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// procNames reads every image name from one Toolhelp snapshot. Unlike OpenProcess it
// works for SYSTEM and other users' processes when the agent is not elevated.
func procNames() map[int32]string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	out := map[int32]string{}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		out[int32(pe.ProcessID)] = windows.UTF16ToString(pe.ExeFile[:])
	}
	return out
}
