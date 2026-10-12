//go:build darwin

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vartio/internal/entities"
)

func listApps() ([]entities.AppInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var paths []string
	paths = append(paths, collectBundles("/Applications", 2)...)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, collectBundles(filepath.Join(home, "Applications"), 1)...)
	}
	for _, root := range []string{"/opt/homebrew/Caskroom", "/usr/local/Caskroom"} {
		paths = append(paths, collectBundles(root, 3)...)
	}
	apps := mapBundles(ctx, paths)
	return finishApps(dedupeMacApps(apps)), nil
}

func collectBundles(root string, depth int) []string {
	var out []string
	walkBundles(root, depth, &out)
	return out
}

func walkBundles(dir string, depth int, out *[]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(dir, name)
		if strings.HasSuffix(name, ".app") {
			*out = append(*out, p)
			continue
		}
		if depth > 0 && e.IsDir() {
			walkBundles(p, depth-1, out)
		}
	}
}

func mapBundles(ctx context.Context, paths []string) []entities.AppInfo {
	var mu sync.Mutex
	var out []entities.AppInfo
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, p := range paths {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			if info, ok := readMacApp(ctx, p); ok {
				mu.Lock()
				out = append(out, info)
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return out
}

func readMacApp(ctx context.Context, path string) (entities.AppInfo, bool) {
	plist := plistStrings(plutilXML(ctx, filepath.Join(path, "Contents", "Info.plist")))
	name := plist["CFBundleDisplayName"]
	if name == "" {
		name = plist["CFBundleName"]
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".app")
	}
	info := entities.AppInfo{
		ID:      plist["CFBundleIdentifier"],
		Name:    name,
		Version: plist["CFBundleShortVersionString"],
		Path:    path,
		Arch:    machOArch(readHead(filepath.Join(path, "Contents", "MacOS", plist["CFBundleExecutable"]))),
		Size:    dirSize(path),
	}
	if info.Version == "" {
		info.Version = plist["CFBundleVersion"]
	}
	if _, err := os.Stat(filepath.Join(path, "Contents", "_MASReceipt", "receipt")); err == nil {
		info.Source = "appstore"
	} else if strings.Contains(path, "/Caskroom/") {
		info.Source = "homebrew"
	}
	added, used := mdlsDates(ctx, path)
	info.Installed = added
	info.LastUsed = used
	return info, info.Name != ""
}

func plutilXML(ctx context.Context, path string) []byte {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", path).Output()
	if err != nil {
		return nil
	}
	return b
}

func readHead(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return buf[:n]
}

func mdlsDates(ctx context.Context, path string) (added, used string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "/usr/bin/mdls", "-name", "kMDItemDateAdded", "-name", "kMDItemLastUsedDate", path).Output()
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "kMDItemDateAdded":
			added = parseMdlsTime(v)
		case "kMDItemLastUsedDate":
			used = parseMdlsTime(v)
		}
	}
	return added, used
}

func dedupeMacApps(apps []entities.AppInfo) []entities.AppInfo {
	cask := map[string]bool{}
	for _, a := range apps {
		if a.Source == "homebrew" && a.ID != "" {
			cask[a.ID] = true
		}
	}
	order := []string{}
	byID := map[string]entities.AppInfo{}
	var bare []entities.AppInfo
	for _, a := range apps {
		if a.ID == "" {
			bare = append(bare, a)
			continue
		}
		if cask[a.ID] && a.Source != "appstore" {
			a.Source = "homebrew"
		}
		prev, ok := byID[a.ID]
		if !ok {
			byID[a.ID] = a
			order = append(order, a.ID)
			continue
		}
		byID[a.ID] = preferMacApp(prev, a)
	}
	out := make([]entities.AppInfo, 0, len(order)+len(bare))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return append(out, bare...)
}

func preferMacApp(a, b entities.AppInfo) entities.AppInfo {
	if strings.Contains(a.Path, "/Caskroom/") && !strings.Contains(b.Path, "/Caskroom/") {
		return mergeMac(b, a)
	}
	if strings.Contains(b.Path, "/Caskroom/") && !strings.Contains(a.Path, "/Caskroom/") {
		return mergeMac(a, b)
	}
	return mergeMac(a, b)
}

func mergeMac(keep, other entities.AppInfo) entities.AppInfo {
	if keep.Source == "" {
		keep.Source = other.Source
	}
	if keep.Version == "" {
		keep.Version = other.Version
	}
	if keep.Size == 0 {
		keep.Size = other.Size
	}
	if keep.LastUsed == "" {
		keep.LastUsed = other.LastUsed
	}
	if keep.Installed == "" {
		keep.Installed = other.Installed
	}
	if keep.Arch == "" {
		keep.Arch = other.Arch
	}
	return keep
}
