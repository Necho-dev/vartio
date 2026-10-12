package agent

import (
	"encoding/binary"
	"strings"
	"testing"

	"vartio/internal/entities"
)

func TestMachOArch(t *testing.T) {
	fat := []byte{
		0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 2,
		0x01, 0x00, 0x00, 0x07, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0x01, 0x00, 0x00, 0x0C, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	if got := machOArch(fat); got != "universal" {
		t.Fatalf("fat %s", got)
	}
	thin := []byte{0xCF, 0xFA, 0xED, 0xFE, 0x0C, 0x00, 0x00, 0x01}
	if got := machOArch(thin); got != "arm64" {
		t.Fatalf("thin %s", got)
	}
}

func TestPlistStrings(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleDisplayName</key><string>比特浏览器</string>
<key>CFBundleShortVersionString</key><string>1.2.3</string>
<key>CFBundleIdentifier</key><string>com.example.app</string>
</dict></plist>`
	got := plistStrings([]byte(xml))
	if got["CFBundleDisplayName"] != "比特浏览器" || got["CFBundleShortVersionString"] != "1.2.3" || got["CFBundleIdentifier"] != "com.example.app" {
		t.Fatalf("%v", got)
	}
}

func TestParseDesktop(t *testing.T) {
	text := `[Desktop Entry]
Name=Firefox
Name[zh_CN]=火狐
Exec=firefox %u
Type=Application
NoDisplay=false

[Desktop Action]
Name=Nope
`
	name, path, _, source, ok := parseDesktop(text)
	if !ok || name != "火狐" || path != "firefox" || source != "" {
		t.Fatalf("%v %s %s %s", ok, name, path, source)
	}
	flat := "[Desktop Entry]\nType=Application\nName=Firefox\nExec=/usr/bin/flatpak run --branch=stable --command=firefox org.mozilla.firefox\n"
	_, execPath, id, source, ok := parseDesktop(flat)
	if !ok || source != "flatpak" || id != "org.mozilla.firefox" || !strings.HasSuffix(execPath, "flatpak") {
		t.Fatalf("%v %s %s %s", ok, execPath, id, source)
	}
	hidden := "[Desktop Entry]\nType=Application\nName=Secret\nNoDisplay=true\nExec=secret\n"
	if _, _, _, _, ok := parseDesktop(hidden); ok {
		t.Fatal("hidden shown")
	}
}

func TestMarkRunning(t *testing.T) {
	apps := []entities.AppInfo{
		{Name: "Cursor", Path: "/Applications/Cursor.app"},
		{Name: "Firefox", Path: "/usr/bin/flatpak", ID: "org.mozilla.firefox", Source: "flatpak"},
		{Name: "Git", Path: `C:\Program Files\Git`},
	}
	procs := []entities.ProcInfo{
		{Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor", CPUPct: 1.5, MemMB: 200},
		{Cmdline: "/Applications/Cursor.app/Contents/Frameworks/Cursor Helper", CPUPct: 0.5, MemMB: 80},
		{Cmdline: "bwrap org.mozilla.firefox", MemMB: 40},
		{Cmdline: `C:\Program Files\GitHub\git.exe`},
		{Cmdline: `C:\Program Files\Git\cmd\git.exe`, MemMB: 12},
	}
	markRunning(apps, procs)
	if !apps[0].Running || apps[0].Procs != 2 || apps[0].MemMB != 280 {
		t.Fatalf("cursor %+v", apps[0])
	}
	if !apps[1].Running || apps[1].Procs != 1 {
		t.Fatalf("flatpak %+v", apps[1])
	}
	if !apps[2].Running || apps[2].Procs != 1 || apps[2].MemMB != 12 {
		t.Fatalf("git %+v", apps[2])
	}
}

func TestVisibleUninstall(t *testing.T) {
	if !visibleUninstall("Git", "", "", 0) {
		t.Fatal("git")
	}
	if visibleUninstall("Update", "parent", "", 0) || visibleUninstall("Hidden", "", "", 1) || visibleUninstall("  ", "", "", 0) {
		t.Fatal("filtered")
	}
}

func TestParseHumanSize(t *testing.T) {
	if parseHumanSize("356 MB") != 356<<20 {
		t.Fatalf("mb %d", parseHumanSize("356 MB"))
	}
	if parseHumanSize("364098 KB") != 364098<<10 {
		t.Fatalf("kb %d", parseHumanSize("364098 KB"))
	}
	pe := make([]byte, 0x40+6)
	pe[0], pe[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(pe[0x3C:], 0x40)
	copy(pe[0x40:], []byte{'P', 'E', 0, 0, 0x64, 0x86})
	if peArchBytes(pe) != "x64" {
		t.Fatalf("pe %s", peArchBytes(pe))
	}
	if parseHumanSize("(null)") != 0 || parseYMD("20260104") == "" || parseYMD("2026") != "" {
		t.Fatal("edges")
	}
}
