//go:build windows

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"govmomi-wmks/internal/guest"
)

var procSHFileOperationW = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// shFileOpStruct is SHFILEOPSTRUCTW with natural alignment, which matches the
// 64-bit SDK layout (the 32-bit header packs it to 1 byte; 386 is not shipped).
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

const (
	foDelete          = 0x3
	fofSilent         = 0x4
	fofNoConfirmation = 0x10
	fofAllowUndo      = 0x40
	fofNoErrorUI      = 0x400
)

// moveToTrash sends abs to the Recycle Bin of the account the agent runs as.
// Only fixed drives have one: elsewhere SHFileOperation would silently delete
// for good, so those are refused.
func moveToTrash(abs string) *guest.FrameError {
	root, err := windows.UTF16PtrFromString(filepath.VolumeName(abs) + `\`)
	if err != nil {
		return bad("invalid path")
	}
	if windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return &guest.FrameError{Code: "not_supported", Message: "this drive has no Recycle Bin"}
	}
	from, err := windows.UTF16FromString(abs)
	if err != nil {
		return bad("invalid path")
	}
	from = append(from, 0) // pFrom is a double-NUL terminated list
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	rc, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	if rc != 0 || op.fAnyOperationsAborted != 0 {
		return &guest.FrameError{Code: "io_error", Message: "recycle failed: " + windows.Errno(rc).Error()}
	}
	if _, err := os.Lstat(abs); err == nil {
		return &guest.FrameError{Code: "io_error", Message: "recycle failed: file still exists"}
	}
	return nil
}

// recycleBins lists X:\$Recycle.Bin\<SID> for each fixed drive, where SID is
// the account the agent runs as.
func recycleBins() ([]string, string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, "", err
	}
	sid := tu.User.Sid.String()
	var bins []string
	for _, root := range fileRoots() {
		p, err := windows.UTF16PtrFromString(root)
		if err != nil || windows.GetDriveType(p) != windows.DRIVE_FIXED {
			continue
		}
		bins = append(bins, filepath.Join(root, "$Recycle.Bin", sid))
	}
	return bins, sid, nil
}

// readRecycleInfo parses a $I file: version (8), size (8), FILETIME (8), then
// a fixed 260-char path (v1) or a length-prefixed path (v2).
func readRecycleInfo(p string) (orig string, size int64, deletedUtc string, ok bool) {
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 24 {
		return "", 0, "", false
	}
	ver := binary.LittleEndian.Uint64(b[0:8])
	size = int64(binary.LittleEndian.Uint64(b[8:16]))
	ft := windows.Filetime{LowDateTime: binary.LittleEndian.Uint32(b[16:20]), HighDateTime: binary.LittleEndian.Uint32(b[20:24])}
	var raw []byte
	switch ver {
	case 1:
		raw = b[24:]
	case 2:
		if len(b) < 28 {
			return "", 0, "", false
		}
		n := int(binary.LittleEndian.Uint32(b[24:28]))
		raw = b[28:]
		if len(raw) > n*2 {
			raw = raw[:n*2]
		}
	default:
		return "", 0, "", false
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	orig = windows.UTF16ToString(u)
	if ft.Nanoseconds() > 0 {
		deletedUtc = time.Unix(0, ft.Nanoseconds()).UTC().Format(time.RFC3339)
	}
	return orig, size, deletedUtc, orig != ""
}

func infoFor(id string) string {
	return filepath.Join(filepath.Dir(id), "$I"+strings.TrimPrefix(filepath.Base(id), "$R"))
}

func trashItems() ([]guest.TrashItem, string, *guest.FrameError) {
	bins, sid, err := recycleBins()
	if err != nil {
		return nil, "", asRPC(err)
	}
	items := []guest.TrashItem{}
	for _, bin := range bins {
		ents, err := os.ReadDir(bin)
		if err != nil {
			continue
		}
		for _, e := range ents {
			name := e.Name()
			if !strings.HasPrefix(name, "$I") {
				continue
			}
			orig, size, deleted, ok := readRecycleInfo(filepath.Join(bin, name))
			if !ok {
				continue
			}
			it, ok := trashEntry(filepath.Join(bin, "$R"+name[2:]), filepath.Base(orig), orig, deleted)
			if !ok {
				continue
			}
			it.Size = size
			items = append(items, it)
		}
	}
	return items, `$Recycle.Bin\` + sid, nil
}

func trashOrig(id string) string {
	orig, _, _, _ := readRecycleInfo(infoFor(id))
	return orig
}

func trashForget(id string) {
	_ = os.Remove(infoFor(id))
}
