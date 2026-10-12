package agent

import (
	"encoding/binary"
	"encoding/xml"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"vartio/internal/entities"
)

// finishApps attaches the current process sample and sorts by name.
// A process sample failure still returns the applications.
func finishApps(apps []entities.AppInfo) []entities.AppInfo {
	if apps == nil {
		apps = []entities.AppInfo{}
	}
	if procs, err := listProcs(); err == nil {
		markRunning(apps, procs)
	}
	sort.Slice(apps, func(i, j int) bool {
		a, b := strings.ToLower(apps[i].Name), strings.ToLower(apps[j].Name)
		if a == b {
			return apps[i].Path < apps[j].Path
		}
		return a < b
	})
	return apps
}

// markRunning counts processes whose executable lives inside the app path,
// or whose command line contains a dotted application id (Flatpak).
func markRunning(apps []entities.AppInfo, procs []entities.ProcInfo) {
	for i := range apps {
		var n int
		var cpu, mem float64
		for _, p := range procs {
			if !appOwns(apps[i], p.Cmdline) {
				continue
			}
			n++
			cpu += p.CPUPct
			mem += p.MemMB
		}
		if n == 0 {
			continue
		}
		apps[i].Running = true
		apps[i].Procs = n
		apps[i].CPUPct = cpu
		apps[i].MemMB = mem
	}
}

func appOwns(a entities.AppInfo, cmd string) bool {
	if pathHit(cmd, a.Path) {
		return true
	}
	id := a.ID
	if len(id) >= 8 && strings.Contains(id, ".") && strings.Contains(cmd, id) {
		return true
	}
	return false
}

func pathHit(cmd, path string) bool {
	cmd = strings.Trim(cmd, `"'`)
	path = strings.TrimRight(path, `/\`)
	if cmd == "" || path == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		cmd = strings.ToLower(strings.ReplaceAll(cmd, "/", `\`))
		path = strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	}
	if cmd == path {
		return true
	}
	if !strings.HasPrefix(cmd, path) {
		return false
	}
	rest := cmd[len(path):]
	return strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, `\`) || strings.HasPrefix(rest, " ")
}

func visibleUninstall(name, parent, parentDisplay string, system uint64) bool {
	return strings.TrimSpace(name) != "" && system != 1 && parent == "" && parentDisplay == ""
}

// machOArch reads a Mach-O or fat header. universal means both arm64 and x86_64.
func machOArch(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	be := binary.BigEndian.Uint32(b[:4])
	switch be {
	case 0xCAFEBABE, 0xCAFEBABF:
		n := binary.BigEndian.Uint32(b[4:8])
		step := 20
		if be == 0xCAFEBABF {
			step = 32
		}
		var arm, intel bool
		for i := uint32(0); i < n && 8+int(i+1)*step <= len(b); i++ {
			off := 8 + int(i)*step
			switch binary.BigEndian.Uint32(b[off : off+4]) {
			case 0x0100000c:
				arm = true
			case 0x01000007:
				intel = true
			}
		}
		return archPair(arm, intel)
	default:
		return machOThin(b)
	}
}

func machOThin(b []byte) string {
	le := binary.LittleEndian.Uint32(b[:4])
	var cpu uint32
	switch le {
	case 0xFEEDFACF, 0xFEEDFACE: // MH_MAGIC_64 / MH_MAGIC
		cpu = binary.LittleEndian.Uint32(b[4:8])
	case 0xCFFAEDFE, 0xCEFAEDFE:
		cpu = binary.BigEndian.Uint32(b[4:8])
	default:
		return ""
	}
	switch cpu {
	case 0x0100000c:
		return "arm64"
	case 0x01000007:
		return "x64"
	case 7:
		return "x86"
	default:
		return ""
	}
}

// peArchBytes reads a PE machine type. x64, x86, and arm64 are the values
// AppInfo stores. A short or non-PE buffer returns an empty string.
func peArchBytes(b []byte) string {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return ""
	}
	off := int(binary.LittleEndian.Uint32(b[0x3C:0x40]))
	if off < 0 || off+6 > len(b) || b[off] != 'P' || b[off+1] != 'E' || b[off+2] != 0 || b[off+3] != 0 {
		return ""
	}
	switch binary.LittleEndian.Uint16(b[off+4 : off+6]) {
	case 0x8664:
		return "x64"
	case 0x14c:
		return "x86"
	case 0xAA64:
		return "arm64"
	default:
		return ""
	}
}

func archPair(arm, intel bool) string {
	switch {
	case arm && intel:
		return "universal"
	case arm:
		return "arm64"
	case intel:
		return "x64"
	default:
		return ""
	}
}

// normalizeArch maps package-manager names onto the AppInfo tokens.
func normalizeArch(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "universal", "fat":
		return "universal"
	case "arm64", "aarch64":
		return "arm64"
	case "x64", "x86_64", "amd64":
		return "x64"
	case "x86", "i386", "i686":
		return "x86"
	case "neutral", "all", "noarch":
		return "neutral"
	default:
		return ""
	}
}

// plistStrings reads the top-level string values of an XML property list.
func plistStrings(b []byte) map[string]string {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	out := map[string]string{}
	var key string
	inKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				inKey = true
				key = ""
			case "string":
				if key != "" {
					var s string
					if dec.DecodeElement(&s, &t) == nil {
						out[key] = s
					}
				}
				key = ""
				inKey = false
			default:
				if t.Name.Local != "plist" && t.Name.Local != "dict" {
					key = ""
				}
				inKey = false
			}
		case xml.CharData:
			if inKey {
				key += string(t)
			}
		case xml.EndElement:
			if t.Name.Local == "key" {
				key = strings.TrimSpace(key)
				inKey = false
			}
		}
	}
}

func parseMdlsTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "(null)" {
		return ""
	}
	t, err := time.Parse("2006-01-02 15:04:05 -0700", s)
	if err != nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func parseYMD(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 8 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	t, err := time.ParseInLocation("20060102", s, time.Local)
	if err != nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// parseDesktop reads one freedesktop entry. ok is false when it is not a
// visible application menu item.
func parseDesktop(text string) (name, execPath, id, source string, ok bool) {
	var names = map[string]string{}
	var execLine, flatpakID, snapName string
	inEntry := false
	typ := "Application"
	noDisplay, hidden := false, false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k, v = strings.TrimSpace(k), desktopUnescape(strings.TrimSpace(v))
		switch {
		case k == "Type":
			typ = v
		case k == "NoDisplay" && v == "true":
			noDisplay = true
		case k == "Hidden" && v == "true":
			hidden = true
		case k == "Exec":
			execLine = v
		case k == "X-Flatpak":
			flatpakID = v
		case k == "X-SnapInstanceName":
			snapName = v
		case strings.HasPrefix(k, "Name"):
			names[k] = v
		}
	}
	if typ != "Application" || noDisplay || hidden {
		return "", "", "", "", false
	}
	name = firstName(names)
	if name == "" {
		return "", "", "", "", false
	}
	execPath, id, source = desktopExec(execLine)
	if flatpakID != "" {
		source = "flatpak"
		id = flatpakID
	}
	if snapName != "" && source == "" {
		source = "snap"
		id = snapName
	}
	if strings.HasSuffix(strings.ToLower(execPath), ".appimage") {
		source = "appimage"
	}
	return name, execPath, id, source, true
}

func firstName(names map[string]string) string {
	for _, k := range []string{"Name[zh_CN]", "Name[zh_Hans]", "Name[zh]", "Name"} {
		if names[k] != "" {
			return names[k]
		}
	}
	return ""
}

func desktopUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}

func desktopExec(line string) (path, id, source string) {
	toks := splitDesktopExec(line)
	if len(toks) > 0 && toks[0] == "env" {
		toks = toks[1:]
		for len(toks) > 0 && strings.Contains(toks[0], "=") && !strings.Contains(toks[0], "/") {
			toks = toks[1:]
		}
	}
	if len(toks) == 0 {
		return "", "", ""
	}
	path = toks[0]
	base := filepath.Base(path)
	if base == "flatpak" {
		source = "flatpak"
		for _, t := range toks[1:] {
			if strings.Count(t, ".") >= 2 && !strings.HasPrefix(t, "-") {
				id = t
			}
		}
	}
	if strings.HasPrefix(path, "/snap/bin/") || strings.Contains(path, "/snapd/") {
		source = "snap"
		if id == "" {
			id = base
		}
	}
	return path, id, source
}

func splitDesktopExec(s string) []string {
	var out []string
	var b strings.Builder
	var quote rune
	flush := func() {
		tok := b.String()
		b.Reset()
		if tok == "" {
			return
		}
		if len(tok) == 2 && tok[0] == '%' && unicode.IsLetter(rune(tok[1])) {
			return
		}
		out = append(out, tok)
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

func parseHumanSize(s string) int64 {
	fs := strings.Fields(strings.TrimSpace(s))
	if len(fs) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(fs[0], 64)
	if err != nil || n <= 0 {
		return 0
	}
	unit := ""
	if len(fs) > 1 {
		unit = strings.ToUpper(fs[1])
	}
	mul := 1.0
	switch unit {
	case "KB", "K":
		mul = 1 << 10
	case "MB", "M":
		mul = 1 << 20
	case "GB", "G":
		mul = 1 << 30
	case "TB", "T":
		mul = 1 << 40
	case "B", "":
		mul = 1
	default:
		return 0
	}
	return int64(n * mul)
}

func dirSize(root string) int64 {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	var n int64
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}
