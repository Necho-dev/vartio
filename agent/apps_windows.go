//go:build windows

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"vartio/internal/entities"
)

func listApps() ([]entities.AppInfo, error) {
	apps := readUninstallApps()
	apps = append(apps, readStoreApps()...)
	return finishApps(dedupeNamed(apps)), nil
}

func dedupeNamed(apps []entities.AppInfo) []entities.AppInfo {
	order := []string{}
	by := map[string]entities.AppInfo{}
	for _, a := range apps {
		k := strings.ToLower(strings.TrimSpace(a.Name))
		if k == "" {
			continue
		}
		prev, ok := by[k]
		if !ok {
			by[k] = a
			order = append(order, k)
			continue
		}
		by[k] = preferNamed(prev, a)
	}
	out := make([]entities.AppInfo, 0, len(order))
	for _, k := range order {
		out = append(out, by[k])
	}
	return out
}

func preferNamed(a, b entities.AppInfo) entities.AppInfo {
	if a.Size > 0 && b.Size == 0 {
		return fillNamed(a, b)
	}
	if b.Size > 0 && a.Size == 0 {
		return fillNamed(b, a)
	}
	if a.Version != "" || b.Version == "" {
		return fillNamed(a, b)
	}
	return fillNamed(b, a)
}

func fillNamed(keep, other entities.AppInfo) entities.AppInfo {
	if keep.Version == "" {
		keep.Version = other.Version
	}
	if keep.Publisher == "" {
		keep.Publisher = other.Publisher
	}
	if keep.Path == "" {
		keep.Path = other.Path
	}
	if keep.Arch == "" {
		keep.Arch = other.Arch
	}
	if keep.Source == "" {
		keep.Source = other.Source
	}
	if keep.Installed == "" {
		keep.Installed = other.Installed
	}
	if keep.ID == "" {
		keep.ID = other.ID
	}
	return keep
}

func readUninstallApps() []entities.AppInfo {
	var out []entities.AppInfo
	out = append(out, uninstallHive(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, registry.WOW64_64KEY, "x64")...)
	out = append(out, uninstallHive(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, registry.WOW64_32KEY, "x86")...)
	out = append(out, uninstallHive(registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, 0, "")...)
	return out
}

func uninstallHive(root registry.Key, path string, wow uint32, arch string) []entities.AppInfo {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|wowAccess(wow))
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var out []entities.AppInfo
	for _, name := range names {
		sk, err := registry.OpenKey(k, name, registry.QUERY_VALUE|wowAccess(wow))
		if err != nil {
			continue
		}
		app, ok := uninstallApp(sk, arch)
		sk.Close()
		if ok {
			out = append(out, app)
		}
	}
	return out
}

func wowAccess(wow uint32) uint32 {
	return wow
}

func uninstallApp(k registry.Key, arch string) (entities.AppInfo, bool) {
	name, _, _ := k.GetStringValue("DisplayName")
	parent, _, _ := k.GetStringValue("ParentKeyName")
	parentDisplay, _, _ := k.GetStringValue("ParentDisplayName")
	system, _, _ := k.GetIntegerValue("SystemComponent")
	if !visibleUninstall(name, parent, parentDisplay, system) {
		return entities.AppInfo{}, false
	}
	ver, _, _ := k.GetStringValue("DisplayVersion")
	pub, _, _ := k.GetStringValue("Publisher")
	loc, _, _ := k.GetStringValue("InstallLocation")
	when, _, _ := k.GetStringValue("InstallDate")
	icon, _, _ := k.GetStringValue("DisplayIcon")
	size, _, _ := k.GetIntegerValue("EstimatedSize")
	exe := iconFile(icon)
	loc = strings.TrimSpace(loc)
	if loc == "" && strings.ContainsAny(exe, `/\`) {
		loc = filepath.Dir(exe)
	}
	arch = peFileArch(exe, arch)
	if arch == "" {
		arch = archFromName(name)
	}
	app := entities.AppInfo{
		Name:      strings.TrimSpace(name),
		Version:   strings.TrimSpace(ver),
		Publisher: strings.TrimSpace(pub),
		Path:      strings.TrimRight(loc, `/\`),
		Arch:      arch,
		Installed: parseYMD(when),
	}
	if size > 0 {
		app.Size = int64(size) * 1024
	}
	return app, true
}

func iconFile(icon string) string {
	icon = strings.Trim(icon, `"`)
	if i := strings.Index(icon, ","); i > 0 {
		icon = icon[:i]
	}
	return strings.TrimSpace(icon)
}

func peFileArch(path, fallback string) string {
	// NSIS and similar uninstallers are often 32-bit even when the app is 64-bit.
	if path == "" || uninstaller(path) || !(strings.HasSuffix(strings.ToLower(path), ".exe") || strings.HasSuffix(strings.ToLower(path), ".dll")) {
		return fallback
	}
	f, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	if arch := peArchBytes(buf[:n]); arch != "" {
		return arch
	}
	return fallback
}

func uninstaller(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "uninstall") || strings.HasPrefix(base, "unins")
}

func archFromName(name string) string {
	n := strings.ToLower(name)
	if strings.Contains(n, "64-bit") || strings.Contains(n, "64 bit") || strings.Contains(name, "64 位") {
		return "x64"
	}
	return ""
}

type storeApp struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	Arch      string `json:"arch"`
	Source    string `json:"source"`
	Path      string `json:"path"`
	ID        string `json:"id"`
	Installed string `json:"installed"`
}

func decodeStoreApps(b []byte) ([]storeApp, error) {
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}))
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	if b[0] == '{' {
		var one storeApp
		if err := json.Unmarshal(b, &one); err != nil {
			return nil, err
		}
		return []storeApp{one}, nil
	}
	var many []storeApp
	if err := json.Unmarshal(b, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func readStoreApps() []entities.AppInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", storeAppScript)
	quiet(cmd)
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	rows, err := decodeStoreApps(b)
	if err != nil {
		return nil
	}
	out := make([]entities.AppInfo, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			continue
		}
		out = append(out, entities.AppInfo{
			ID:        r.ID,
			Name:      r.Name,
			Version:   r.Version,
			Publisher: r.Publisher,
			Arch:      normalizeArch(r.Arch),
			Source:    strings.ToLower(r.Source),
			Path:      r.Path,
			Installed: r.Installed,
		})
	}
	return out
}

const storeAppScript = `
$ErrorActionPreference = 'SilentlyContinue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$OutputEncoding = [Console]::OutputEncoding
$starts = @{}
Get-StartApps | ForEach-Object { $starts[$_.AppID] = $_.Name }
$rows = @()
Get-AppxPackage | Where-Object { -not $_.IsFramework -and -not $_.IsResourcePackage -and ($_.SignatureKind -eq 'Store' -or $_.SignatureKind -eq 'Developer') } | ForEach-Object {
  $fam = $_.PackageFamilyName
  $disp = ''
  foreach ($k in @($starts.Keys)) {
    if ($k.StartsWith($fam + '!') -or $k -eq $fam) { $disp = [string]$starts[$k]; break }
  }
  if (-not $disp) { return }
  $pub = [string]$_.Publisher
  if ($pub -match 'CN=([^,]+)') { $pub = $Matches[1] }
  $born = ''
  if ($_.InstallLocation -and (Test-Path -LiteralPath $_.InstallLocation)) {
    $born = (Get-Item -LiteralPath $_.InstallLocation).CreationTime.ToString('o')
  }
  $rows += [pscustomobject]@{
    name = $disp
    version = [string]$_.Version
    publisher = $pub
    arch = [string]$_.Architecture
    source = ([string]$_.SignatureKind).ToLower()
    path = [string]$_.InstallLocation
    id = [string]$fam
    installed = $born
  }
}
if ($rows.Count -eq 0) { '[]' } else { $rows | ConvertTo-Json -Compress }
`
