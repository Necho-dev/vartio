//go:build linux

package agent

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"vartio/internal/entities"
)

func listApps() ([]entities.AppInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var found []linuxApp
	for _, dir := range desktopDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".desktop") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			name, execPath, id, source, ok := parseDesktop(string(b))
			if !ok {
				continue
			}
			if source == "" {
				source = sourceFromPath(p)
			}
			if source == "snap" && id == "" {
				id = snapName(p, execPath)
			}
			found = append(found, linuxApp{
				app: entities.AppInfo{
					ID: id, Name: name, Path: execPath, Source: source,
				},
				desktop: p,
			})
		}
	}
	enrichDeb(ctx, found)
	enrichRPM(ctx, found)
	enrichFlatpak(ctx, found)
	enrichSnap(ctx, found)
	apps := make([]entities.AppInfo, 0, len(found))
	for _, f := range found {
		if f.app.Path == "" {
			f.app.Path = f.desktop
		}
		apps = append(apps, f.app)
	}
	return finishApps(apps), nil
}

type linuxApp struct {
	app     entities.AppInfo
	desktop string
}

func desktopDirs() []string {
	dirs := []string{
		"/usr/share/applications",
		"/usr/local/share/applications",
		"/var/lib/flatpak/exports/share/applications",
		"/var/lib/snapd/desktop/applications",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local/share/applications"),
			filepath.Join(home, ".local/share/flatpak/exports/share/applications"),
		)
	}
	return dirs
}

func sourceFromPath(p string) string {
	switch {
	case strings.Contains(p, "/flatpak/"):
		return "flatpak"
	case strings.Contains(p, "/snapd/") || strings.Contains(p, "/snap/"):
		return "snap"
	default:
		return ""
	}
}

func snapName(desktop, execPath string) string {
	base := strings.TrimSuffix(filepath.Base(desktop), ".desktop")
	if i := strings.Index(base, "_"); i > 0 {
		return base[:i]
	}
	if execPath != "" {
		return filepath.Base(execPath)
	}
	return base
}

type pkgMeta struct {
	version string
	arch    string
	size    int64
}

func enrichDeb(ctx context.Context, apps []linuxApp) {
	if _, err := exec.LookPath("dpkg-query"); err != nil {
		return
	}
	meta := dpkgMeta(ctx)
	owner := dpkgOwners(ctx)
	for i := range apps {
		if apps[i].app.Source != "" && apps[i].app.Source != "deb" {
			continue
		}
		pkg := owner[apps[i].desktop]
		m, ok := meta[pkg]
		if !ok {
			continue
		}
		apps[i].app.Source = "deb"
		apps[i].app.Version = m.version
		apps[i].app.Arch = normalizeArch(m.arch)
		apps[i].app.Size = m.size
		if apps[i].app.ID == "" {
			apps[i].app.ID = pkg
		}
	}
}

func dpkgMeta(ctx context.Context) map[string]pkgMeta {
	b, err := commandOut(ctx, "dpkg-query", "-W", "-f", `${Package}\t${Version}\t${Installed-Size}\t${Architecture}\n`)
	if err != nil {
		return nil
	}
	out := map[string]pkgMeta{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		var size int64
		if n := parseHumanSize(f[2] + " KB"); n > 0 {
			size = n
		}
		out[f[0]] = pkgMeta{version: f[1], arch: f[3], size: size}
	}
	return out
}

func dpkgOwners(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, dir := range []string{"/usr/share/applications", "/usr/local/share/applications"} {
		b, err := commandOut(ctx, "dpkg-query", "-S", dir)
		if err != nil && len(b) == 0 {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			pkg, path, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			if i := strings.Index(pkg, ","); i >= 0 {
				pkg = pkg[:i]
			}
			out[strings.TrimSpace(path)] = strings.TrimSpace(pkg)
		}
	}
	return out
}

func enrichRPM(ctx context.Context, apps []linuxApp) {
	if _, err := exec.LookPath("rpm"); err != nil {
		return
	}
	for i := range apps {
		if apps[i].app.Source != "" {
			continue
		}
		b, err := commandOut(ctx, "rpm", "-qf", "--qf", `%{NAME}\t%{VERSION}\t%{SIZE}\t%{ARCH}`, apps[i].desktop)
		if err != nil || bytes.Contains(b, []byte("not owned")) {
			continue
		}
		f := strings.Split(strings.TrimSpace(string(b)), "\t")
		if len(f) < 4 {
			continue
		}
		var size int64
		if n, err := parseInt64(f[2]); err == nil {
			size = n
		}
		apps[i].app.Source = "rpm"
		apps[i].app.Version = f[1]
		apps[i].app.Arch = normalizeArch(f[3])
		apps[i].app.Size = size
		if apps[i].app.ID == "" {
			apps[i].app.ID = f[0]
		}
	}
}

func enrichFlatpak(ctx context.Context, apps []linuxApp) {
	if _, err := exec.LookPath("flatpak"); err != nil {
		return
	}
	b, err := commandOut(ctx, "flatpak", "list", "--app", "--columns=application,version,arch,origin,size")
	if err != nil && len(b) == 0 {
		return
	}
	meta := map[string]pkgMeta{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 5 || strings.Contains(f[0], " ") {
			continue
		}
		meta[f[0]] = pkgMeta{version: f[1], arch: f[2], size: parseHumanSize(f[4])}
	}
	for i := range apps {
		if apps[i].app.Source != "flatpak" {
			continue
		}
		m, ok := meta[apps[i].app.ID]
		if !ok {
			continue
		}
		apps[i].app.Version = m.version
		apps[i].app.Arch = normalizeArch(m.arch)
		apps[i].app.Size = m.size
	}
}

func enrichSnap(ctx context.Context, apps []linuxApp) {
	if _, err := exec.LookPath("snap"); err != nil {
		return
	}
	b, err := commandOut(ctx, "snap", "list")
	if err != nil && len(b) == 0 {
		return
	}
	meta := map[string]pkgMeta{}
	pub := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		meta[f[0]] = pkgMeta{version: f[1]}
		if len(f) >= 5 {
			pub[f[0]] = strings.TrimRight(f[4], "*")
		}
	}
	for i := range apps {
		if apps[i].app.Source != "snap" {
			continue
		}
		m, ok := meta[apps[i].app.ID]
		if !ok {
			continue
		}
		apps[i].app.Version = m.version
		if apps[i].app.Publisher == "" {
			apps[i].app.Publisher = pub[apps[i].app.ID]
		}
	}
}

func commandOut(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	quiet(cmd)
	return cmd.Output()
}

func parseInt64(s string) (int64, error) {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}
