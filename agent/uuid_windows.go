//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// readBIOSUUID prefers the SMBIOS UUID (matches ESXi biosUuid). Physical boards that ship
// a placeholder UUID fall back to MachineGuid so LAN machines do not collide.
func readBIOSUUID() (string, error) {
	id, err := readSMBIOSUUID()
	if err == nil && !bogusUUID(id) {
		return id, nil
	}
	if guid := readMachineID(); guid != "" {
		return guid, nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func readHardwareUUID() string {
	id, err := readSMBIOSUUID()
	if err != nil || bogusUUID(id) {
		return ""
	}
	return id
}

func readMachineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(guid))
}

func bogusUUID(id string) bool {
	s := strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if s == "" || strings.Trim(s, "0") == "" || strings.Trim(s, "f") == "" {
		return true
	}
	return s == "03000200040005000006000700080009"
}

func readSMBIOSUUID() (string, error) {
	mod := windows.NewLazySystemDLL("kernel32.dll")
	proc := mod.NewProc("GetSystemFirmwareTable")
	const rsmb = 0x52534D42
	n, _, _ := proc.Call(rsmb, 0, 0, 0)
	if n == 0 {
		return "", errNoSMBIOS
	}
	buf := make([]byte, n)
	r, _, err := proc.Call(rsmb, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(n))
	if r == 0 {
		return "", err
	}
	if len(buf) < 8 {
		return "", errNoSMBIOS
	}
	data := buf[8:]
	off := 0
	for off+8 <= len(data) {
		typ := data[off]
		length := int(data[off+1])
		if length < 4 || off+length > len(data) {
			break
		}
		if typ == 1 && length >= 24 {
			u := data[off+8 : off+24]
			// SMBIOS UUID little-endian for first three fields
			return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
				binary.LittleEndian.Uint32(u[0:4]),
				binary.LittleEndian.Uint16(u[4:6]),
				binary.LittleEndian.Uint16(u[6:8]),
				u[8], u[9], u[10], u[11], u[12], u[13], u[14], u[15]), nil
		}
		next := off + length
		for next < len(data)-1 {
			if data[next] == 0 && data[next+1] == 0 {
				off = next + 2
				break
			}
			next++
		}
		if next >= len(data)-1 {
			break
		}
	}
	return "", errNoSMBIOS
}
