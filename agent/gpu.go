package agent

import (
	"context"
	"encoding/csv"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"vartio/internal/entities"
)

// gpus is one sample of every graphics device this host can see.
// A missing tool is remembered for a while; a present tool is read every sample.
// The read finishes before the sample is encoded. Leaving it running in the
// background planted a raw NUL in the reply JSON and dropped the websocket.
func gpus(ctx context.Context) []entities.GPUInfo {
	return platformGPUs(ctx)
}

var nvidiaBin struct {
	sync.Mutex
	path  string
	until time.Time
}

func nvidiaBinary() string {
	nvidiaBin.Lock()
	defer nvidiaBin.Unlock()
	if time.Now().Before(nvidiaBin.until) {
		return nvidiaBin.path
	}
	nvidiaBin.until = time.Now().Add(10 * time.Minute)
	nvidiaBin.path = findNvidiaSmi()
	return nvidiaBin.path
}

func findNvidiaSmi() string {
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		return p
	}
	if runtime.GOOS != "windows" {
		return ""
	}
	for _, p := range []string{
		`C:\Windows\System32\nvidia-smi.exe`,
		`C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func nvidiaGPUs(ctx context.Context) []entities.GPUInfo {
	bin := nvidiaBinary()
	if bin == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin,
		"--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,clocks.sm",
		"--format=csv,noheader,nounits")
	quiet(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseNvidiaSMI(string(out))
}

func parseNvidiaSMI(out string) []entities.GPUInfo {
	r := csv.NewReader(strings.NewReader(out))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil
	}
	var gpus []entities.GPUInfo
	for _, row := range rows {
		if len(row) < 4 {
			continue
		}
		name := strings.TrimSpace(row[0])
		if name == "" || strings.Contains(name, "NVIDIA-SMI") {
			continue
		}
		g := entities.GPUInfo{Name: name, Vendor: "nvidia"}
		if v, ok := parseGPUFloat(row[1]); ok {
			g.UtilPct = v
		}
		if v, ok := parseGPUFloat(row[2]); ok {
			g.MemUsed = uint64(v * 1024 * 1024)
		}
		if v, ok := parseGPUFloat(row[3]); ok {
			g.MemTotal = uint64(v * 1024 * 1024)
		}
		if len(row) > 4 {
			g.TempC = gpuFloatPtr(row[4])
		}
		if len(row) > 5 {
			g.PowerW = gpuFloatPtr(row[5])
		}
		if len(row) > 6 {
			g.ClockMHz = gpuFloatPtr(row[6])
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// peakGPUs keeps the busiest reading of each counter when several snapshots of the
// same device are merged. Apple's IORegistry percentages are instantaneous and
// often read zero between frames, so one unlucky sample understates the GPU.
func peakGPUs(base, sample []entities.GPUInfo) []entities.GPUInfo {
	if len(base) == 0 {
		return append([]entities.GPUInfo(nil), sample...)
	}
	idx := map[string]int{}
	for i, g := range base {
		idx[g.Name] = i
	}
	for _, s := range sample {
		i, ok := idx[s.Name]
		if !ok {
			base = append(base, s)
			idx[s.Name] = len(base) - 1
			continue
		}
		g := &base[i]
		if s.UtilPct > g.UtilPct {
			g.UtilPct = s.UtilPct
		}
		g.RenderPct = maxGPUPct(g.RenderPct, s.RenderPct)
		g.TilerPct = maxGPUPct(g.TilerPct, s.TilerPct)
		if s.MemUsed > g.MemUsed {
			g.MemUsed = s.MemUsed
		}
		if g.Cores == 0 {
			g.Cores = s.Cores
		}
	}
	return base
}

// mergeWindowsGPUs keeps every adapter. nvidia-smi supplies the name, VRAM,
// temperature and power; the performance counters supply Task Manager's
// utilization and any GPU nvidia-smi does not list (the Intel iGPU).
func mergeWindowsGPUs(nv, pdh []entities.GPUInfo) []entities.GPUInfo {
	if len(nv) == 0 {
		return pdh
	}
	if len(pdh) == 0 {
		return nv
	}
	used := make([]bool, len(pdh))
	out := append([]entities.GPUInfo(nil), nv...)
	for i := range out {
		j := matchDedicated(out[i].MemTotal, pdh, used)
		if j < 0 {
			continue
		}
		used[j] = true
		if pdh[j].UtilPct > out[i].UtilPct {
			out[i].UtilPct = pdh[j].UtilPct
		}
		if out[i].MemUsed == 0 {
			out[i].MemUsed = pdh[j].MemUsed
		}
		if out[i].MemTotal == 0 {
			out[i].MemTotal = pdh[j].MemTotal
		}
	}
	for i, p := range pdh {
		if !used[i] {
			out = append(out, p)
		}
	}
	return out
}

func matchDedicated(total uint64, pdh []entities.GPUInfo, used []bool) int {
	if total == 0 {
		return -1
	}
	best, bestDelta := -1, uint64(1<<64-1)
	for i, p := range pdh {
		if used[i] || p.MemTotal == 0 {
			continue
		}
		d := total - p.MemTotal
		if p.MemTotal > total {
			d = p.MemTotal - total
		}
		closeEnough := d < 1<<30 || d*100/total < 15
		if closeEnough && d < bestDelta {
			best, bestDelta = i, d
		}
	}
	return best
}

// assignGPUNames replaces generic "GPU N" labels with adapter names that are
// not already used by a named device.
func assignGPUNames(gs []entities.GPUInfo, adapters []string) []entities.GPUInfo {
	used := map[string]bool{}
	for _, g := range gs {
		if !genericGPUName(g.Name) {
			used[strings.ToLower(g.Name)] = true
		}
	}
	var spare []string
	for _, a := range adapters {
		a = strings.TrimSpace(a)
		if a == "" || used[strings.ToLower(a)] || adapterAlreadyNamed(a, gs) {
			continue
		}
		spare = append(spare, a)
	}
	j := 0
	for i := range gs {
		if !genericGPUName(gs[i].Name) || j >= len(spare) {
			continue
		}
		gs[i].Name = spare[j]
		j++
		switch {
		case strings.Contains(strings.ToLower(gs[i].Name), "intel"):
			gs[i].Vendor = "intel"
		case strings.Contains(strings.ToLower(gs[i].Name), "nvidia"):
			gs[i].Vendor = "nvidia"
		case strings.Contains(strings.ToLower(gs[i].Name), "amd"), strings.Contains(strings.ToLower(gs[i].Name), "radeon"):
			gs[i].Vendor = "amd"
		}
	}
	return gs
}

func genericGPUName(name string) bool {
	return name == "" || strings.HasPrefix(name, "GPU ")
}

func adapterAlreadyNamed(adapter string, gs []entities.GPUInfo) bool {
	a := strings.ToLower(adapter)
	for _, g := range gs {
		n := strings.ToLower(g.Name)
		if genericGPUName(g.Name) {
			continue
		}
		if n == a || strings.Contains(n, a) || strings.Contains(a, n) {
			return true
		}
	}
	return false
}

// dropIdleAdapters removes performance-counter nodes that never report memory
// or utilization and never received a real adapter name. Windows publishes a
// few of these beside the Intel and NVIDIA devices.
func dropIdleAdapters(gs []entities.GPUInfo) []entities.GPUInfo {
	out := gs[:0]
	for _, g := range gs {
		if genericGPUName(g.Name) && g.UtilPct == 0 && g.MemUsed == 0 && g.MemTotal == 0 && g.TempC == nil {
			continue
		}
		out = append(out, g)
	}
	return out
}

func maxGPUPct(a, b *float64) *float64 {
	if a == nil {
		return b
	}
	if b == nil || *a >= *b {
		return a
	}
	return b
}

func parseGPUFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "N/A") || strings.Contains(s, "Not Supported") {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func gpuFloatPtr(s string) *float64 {
	v, ok := parseGPUFloat(s)
	if !ok {
		return nil
	}
	return &v
}

// parseIOReg reads `ioreg -r -c IOAccelerator -d 1 -k PerformanceStatistics`.
// Apple reports device, renderer and tiler utilization plus unified-memory use.
// Allocated bytes are not a VRAM capacity, so MemTotal stays empty.
func parseIOReg(text string) []entities.GPUInfo {
	parts := strings.Split(text, "\n+-o ")
	var out []entities.GPUInfo
	for _, part := range parts {
		stats := ioregDict(part, "PerformanceStatistics")
		if stats == "" {
			continue
		}
		vals := map[string]float64{}
		for _, m := range reIORegStat.FindAllStringSubmatch(stats, -1) {
			v, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				continue
			}
			vals[m[1]] = v
		}
		util, ok := vals["Device Utilization %"]
		if !ok {
			continue
		}
		g := entities.GPUInfo{UtilPct: util, Vendor: "apple"}
		if m := reIORegModel.FindStringSubmatch(part); len(m) == 2 {
			g.Name = m[1]
		}
		if g.Name == "" {
			if m := reIORegClass.FindStringSubmatch(part); len(m) == 2 {
				g.Name = m[1]
			}
		}
		if g.Name == "" {
			g.Name = "GPU"
		}
		if !strings.Contains(part, "AGX") && !strings.Contains(g.Name, "Apple") {
			g.Vendor = ""
		}
		if v, ok := vals["Renderer Utilization %"]; ok {
			g.RenderPct = &v
		}
		if v, ok := vals["Tiler Utilization %"]; ok {
			g.TilerPct = &v
		}
		if v, ok := vals["In use system memory"]; ok && v > 0 {
			g.MemUsed = uint64(v)
		}
		if m := reIORegCores.FindStringSubmatch(part); len(m) == 2 {
			g.Cores, _ = strconv.Atoi(m[1])
		}
		out = append(out, g)
	}
	return out
}

var (
	reIORegStat  = regexp.MustCompile(`"([^"]+)"=(-?\d+(?:\.\d+)?)`)
	reIORegModel = regexp.MustCompile(`"model" = "([^"]+)"`)
	reIORegClass = regexp.MustCompile(`"IOClass" = "([^"]+)"`)
	reIORegCores = regexp.MustCompile(`"gpu-core-count" = (\d+)`)
)

func ioregDict(part, key string) string {
	i := strings.Index(part, `"`+key+`" = {`)
	if i < 0 {
		return ""
	}
	rest := part[i+len(key)+6:]
	depth := 1
	for n, r := range rest {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[:n]
			}
		}
	}
	return ""
}

// reGPUPhys pulls the adapter identity out of a Windows GPU performance-counter instance.
var reGPUPhys = regexp.MustCompile(`luid_0x[0-9a-fA-F]+_0x[0-9a-fA-F]+_phys_\d+`)

type gpuCounter struct {
	Instance string
	Value    float64
}

// gpusFromCounters groups engine utilizations and dedicated memory by adapter.
// Utilization is the busiest engine on that adapter, not the sum.
func gpusFromCounters(engines, used, limit []gpuCounter) []entities.GPUInfo {
	type acc struct {
		util  float64
		has   bool
		mem   uint64
		total uint64
	}
	by := map[string]*acc{}
	touch := func(inst string) *acc {
		key := reGPUPhys.FindString(inst)
		if key == "" {
			return nil
		}
		a := by[key]
		if a == nil {
			a = &acc{}
			by[key] = a
		}
		return a
	}
	for _, e := range engines {
		a := touch(e.Instance)
		if a == nil || e.Value < 0 {
			continue
		}
		if !a.has || e.Value > a.util {
			a.util = e.Value
			a.has = true
		}
	}
	for _, e := range used {
		if a := touch(e.Instance); a != nil && e.Value > 0 {
			a.mem = uint64(e.Value)
		}
	}
	for _, e := range limit {
		if a := touch(e.Instance); a != nil && e.Value > 0 {
			a.total = uint64(e.Value)
		}
	}
	if len(by) == 0 {
		return nil
	}
	keys := make([]string, 0, len(by))
	for k, a := range by {
		if a.has || a.mem > 0 || a.total > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]entities.GPUInfo, 0, len(keys))
	for i, k := range keys {
		a := by[k]
		g := entities.GPUInfo{Name: "GPU " + strconv.Itoa(i), UtilPct: a.util, MemUsed: a.mem, MemTotal: a.total}
		if a.util > 100 {
			g.UtilPct = 100
		}
		out = append(out, g)
	}
	return out
}

// drmGPUs reads Linux DRM sysfs. root is /sys/class/drm on a real host.
// NVIDIA cards are included; the caller drops them when nvidia-smi already answered.
func drmGPUs(root string) []entities.GPUInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if reDRMCard.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []entities.GPUInfo
	for _, name := range names {
		dev := root + "/" + name + "/device"
		vendor := strings.TrimSpace(readTrim(dev + "/vendor"))
		busy, hasBusy := parseGPUFloat(readTrim(dev + "/gpu_busy_percent"))
		memUsed, hasUsed := parseGPUUint(readTrim(dev + "/mem_info_vram_used"))
		memTotal, hasTotal := parseGPUUint(readTrim(dev + "/mem_info_vram_total"))
		if !hasBusy && !hasUsed && !hasTotal {
			continue
		}
		g := entities.GPUInfo{Name: drmName(dev, vendor), Vendor: drmVendor(vendor)}
		if hasBusy {
			g.UtilPct = busy
		}
		if hasUsed {
			g.MemUsed = memUsed
		}
		if hasTotal {
			g.MemTotal = memTotal
		}
		if t, ok := drmTemp(dev); ok {
			g.TempC = &t
		}
		out = append(out, g)
	}
	return out
}

var reDRMCard = regexp.MustCompile(`^card\d+$`)

func drmVendor(id string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "0x")) {
	case "10de":
		return "nvidia"
	case "1002":
		return "amd"
	case "8086":
		return "intel"
	default:
		return ""
	}
}

func drmName(dev, vendor string) string {
	if m := reDRMDriver.FindStringSubmatch(readTrim(dev + "/uevent")); len(m) == 2 && m[1] != "" {
		return m[1]
	}
	switch drmVendor(vendor) {
	case "nvidia":
		return "NVIDIA"
	case "amd":
		return "AMD"
	case "intel":
		return "Intel"
	default:
		return "GPU"
	}
}

var reDRMDriver = regexp.MustCompile(`(?m)^DRIVER=(.+)$`)

func drmTemp(dev string) (float64, bool) {
	hw, err := os.ReadDir(dev + "/hwmon")
	if err != nil {
		return 0, false
	}
	for _, e := range hw {
		raw := readTrim(dev + "/hwmon/" + e.Name() + "/temp1_input")
		v, ok := parseGPUFloat(raw)
		if !ok {
			continue
		}
		c := v / 1000
		if c > 0 && c < 150 {
			return c, true
		}
	}
	return 0, false
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func parseGPUUint(s string) (uint64, bool) {
	v, ok := parseGPUFloat(s)
	if !ok || v < 0 {
		return 0, false
	}
	return uint64(v), true
}
