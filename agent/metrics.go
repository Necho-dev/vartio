package agent

import (
	"context"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/shirou/gopsutil/v4/sensors"

	"vartio/internal/entities"
)

const maxTemps = 12

var sysInfoOnce struct {
	sync.Once
	v entities.SysInfo
}

func staticInfo() entities.SysInfo {
	sysInfoOnce.Do(func() {
		v := entities.SysInfo{HostName: hostname(), OS: runtime.GOOS, Arch: runtime.GOARCH}
		if hi, err := host.Info(); err == nil {
			v.Platform = hi.Platform
			v.PlatformVersion = hi.PlatformVersion
			v.Kernel = hi.KernelVersion
			if hi.KernelArch != "" {
				v.Arch = hi.KernelArch
			}
		}
		if info, err := cpu.Info(); err == nil && len(info) > 0 {
			v.CPUModel = strings.TrimSpace(info[0].ModelName)
		}
		v.Vendor, v.Product, v.Virt = virtualization()
		v.Cores, _ = cpu.Counts(false)
		v.Threads, _ = cpu.Counts(true)
		sysInfoOnce.v = v
	})
	v := sysInfoOnce.v
	v.HostName = hostname()
	if bt, err := host.BootTime(); err == nil {
		v.BootTime = int64(bt)
	}
	return v
}

// metricsRead keeps a slow snapshot from blocking the next one. A collection
// already running is answered with the previous snapshot, so one stuck sample
// cannot pile up. The first sample, which has nothing to reuse, is waited out
// instead of being published as an empty reading.
var metricsRead limitedRead[entities.Metrics]

func collectMetrics() entities.Metrics {
	m, ok := metricsRead.get(4*time.Second, collectSnapshot)
	if ok {
		return m
	}
	m, ok = metricsRead.get(2*time.Minute, collectSnapshot)
	if ok {
		return m
	}
	return entities.Metrics{TimeMs: time.Now().UnixMilli(), Info: staticInfo()}
}

func collectSnapshot() entities.Metrics {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out := entities.Metrics{TimeMs: time.Now().UnixMilli(), Info: staticInfo()}
	var containers chan *entities.DockerInfo
	if docker() != nil {
		containers = make(chan *entities.DockerInfo, 1)
		go func() {
			c, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
			defer cancel()
			containers <- dockerMetrics(c)
		}()
	}
	out.CPUPct, out.CPUUserPct, out.CPUSystemPct, out.CPUWaitPct, out.Cores = sampleCPU(ctx)
	if l, ok := loadAvg(ctx); ok {
		out.Load = l
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		out.MemTotal = vm.Total
		out.MemUsed = vm.Total - vm.Available
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		out.SwapTotal, out.SwapUsed = sw.Total, sw.Used
	}
	out.Disks, out.RootMount = diskUsages(ctx)
	out.DiskRead, out.DiskWrite = diskIO(ctx)
	out.NetRx, out.NetTx, out.NetPkts = netIO(ctx)
	if t, err := tcpStats(ctx); err == nil {
		out.TCP = t
	}
	out.NICs = nicList(ctx)
	out.Temps = temps(ctx)
	out.GPUs = gpus(ctx)
	out.Battery = readBattery()
	out.Temps = appendGPUTemps(out.Temps, out.GPUs)
	if pids, err := process.PidsWithContext(ctx); err == nil {
		out.Procs = len(pids)
	}
	if containers != nil {
		out.Docker = <-containers
	}
	return out
}

func diskUsages(ctx context.Context) ([]entities.DiskUsage, string) {
	parts, _ := disk.PartitionsWithContext(ctx, false)
	seen := map[string]bool{}
	var out []entities.DiskUsage
	for _, p := range parts {
		if skipMount(p) || seen[p.Device] {
			continue
		}
		u, err := disk.UsageWithContext(ctx, hostPath(p.Mountpoint))
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, entities.DiskUsage{Mount: p.Mountpoint, FS: p.Fstype, Total: u.Total, Used: u.Used})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	root := pickRoot(out)
	return dedupePools(out, root), root
}

func dedupePools(disks []entities.DiskUsage, root string) []entities.DiskUsage {
	return entities.DedupePools(disks, root)
}

func pickRoot(disks []entities.DiskUsage) string {
	want := []string{"/"}
	switch runtime.GOOS {
	case "windows":
		sd := os.Getenv("SystemDrive")
		if sd == "" {
			sd = "C:"
		}
		want = []string{sd, sd + `\`}
	case "darwin":
		want = []string{"/System/Volumes/Data", "/"}
	}
	pref := ""
	for _, w := range want {
		for _, d := range disks {
			if strings.EqualFold(d.Mount, w) {
				pref = d.Mount
				break
			}
		}
		if pref != "" {
			break
		}
	}
	// "/" is often an overlay or an initrd inside a container. Fall through to
	// the largest writable disk (Docker Desktop: /var/lib, not /oldroot).
	return entities.CapacityMount(disks, pref)
}

func skipMount(p disk.PartitionStat) bool {
	m := p.Mountpoint
	switch strings.ToLower(p.Fstype) {
	case "devfs", "autofs", "nullfs", "tmpfs", "devtmpfs", "squashfs", "overlay":
		return true
	}
	switch runtime.GOOS {
	case "darwin":
		if strings.HasPrefix(m, "/System/Volumes/") && m != "/System/Volumes/Data" {
			return true
		}
		if strings.HasPrefix(m, "/Library/Developer/") || strings.HasPrefix(m, "/private/var/") {
			return true
		}
	case "linux":
		if strings.HasPrefix(m, "/boot") || strings.HasPrefix(m, "/snap") || strings.HasPrefix(m, "/var/lib/docker") {
			return true
		}
	}
	return skipMountOS(p)
}

func diskIO(ctx context.Context) (read, write uint64) {
	cs, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return 0, 0
	}
	for name, c := range cs {
		if skipIODevice(name, cs) {
			continue
		}
		read += c.ReadBytes
		write += c.WriteBytes
	}
	return read, write
}

// skipIODevice drops partitions whose parent disk is also listed, plus loop/ram devices,
// so the same bytes are not counted twice.
func skipIODevice(name string, all map[string]disk.IOCountersStat) bool {
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") {
		return true
	}
	for other := range all {
		if other != name && len(other) >= 3 && strings.HasPrefix(name, other) {
			return true
		}
	}
	return false
}

func skipNIC(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"lo", "utun", "awdl", "llw", "gif", "stf", "anpi", "bridge", "docker", "veth", "br-", "virbr", "tailscale", "zt"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range []string{"loopback", "isatap", "teredo", "pseudo", "bluetooth", "vethernet", "vmware", "virtualbox", "hyper-v"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

func netIO(ctx context.Context) (rx, tx uint64, pk *entities.NetPackets) {
	cs, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return 0, 0, nil
	}
	pk = &entities.NetPackets{}
	for _, c := range cs {
		if skipNIC(c.Name) {
			continue
		}
		rx += c.BytesRecv
		tx += c.BytesSent
		nrx, ntx := nonUnicastPkts(c.Name)
		pk.Rx += c.PacketsRecv + nrx
		pk.Tx += c.PacketsSent + ntx
		pk.ErrIn += c.Errin
		pk.ErrOut += c.Errout
		pk.DropIn += c.Dropin
		pk.DropOut += c.Dropout
	}
	return rx, tx, pk
}

func nicList(ctx context.Context) []entities.NICInfo {
	ifaces, err := net.InterfacesWithContext(ctx)
	if err != nil {
		return nil
	}
	var out []entities.NICInfo
	for _, iface := range ifaces {
		if skipNIC(iface.Name) {
			continue
		}
		n := entities.NICInfo{Name: iface.Name, MAC: iface.HardwareAddr, Up: hasFlag(iface.Flags, "up")}
		for _, a := range iface.Addrs {
			if strings.Contains(a.Addr, ".") && !strings.HasPrefix(a.Addr, "127.") && !strings.HasPrefix(a.Addr, "169.254.") {
				n.IPv4 = strings.Split(a.Addr, "/")[0]
				break
			}
		}
		if n.IPv4 == "" {
			continue
		}
		out = append(out, n)
	}
	if cs, err := net.IOCountersWithContext(ctx, true); err == nil {
		by := make(map[string]net.IOCountersStat, len(cs))
		for _, c := range cs {
			by[c.Name] = c
		}
		for i := range out {
			if c, ok := by[out[i].Name]; ok {
				out[i].Rx, out[i].Tx = c.BytesRecv, c.BytesSent
			}
		}
	}
	return out
}

var tempsState struct {
	sync.Mutex
	off  bool
	has  bool
	at   time.Time
	last []entities.TempSensor
}

// One refresh at a time. A query that ignores its deadline cannot stack up.
var tempRead limitedRead[[]entities.TempSensor]

// temps is best effort and cached for a minute, including an empty reading.
// Windows reads ACPI thermal zones, which a desktop often does not publish, so
// an empty read is retried next minute instead of turning the sensor off.
// macOS reads IOHID sensors (CPU die, GPU, NAND, battery); GUEST_TEMPS=0 turns
// that off. GPU core temperature is added separately. A refresh still running
// returns the previous minute's values instead of blocking this sample.
func temps(ctx context.Context) []entities.TempSensor {
	tempsState.Lock()
	if tempsState.off {
		last := tempsState.last
		tempsState.Unlock()
		return last
	}
	if runtime.GOOS == "darwin" && getenv("VARTIO_AGENT_TEMPS", "GUEST_TEMPS") == "0" {
		tempsState.off = true
		tempsState.Unlock()
		return nil
	}
	if tempsState.has && time.Since(tempsState.at) < time.Minute {
		last := append([]entities.TempSensor(nil), tempsState.last...)
		tempsState.Unlock()
		return last
	}
	tempsState.Unlock()

	wait := time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d < wait {
			wait = d
		}
	}
	if wait < 0 {
		wait = 0
	}
	out, _ := tempRead.get(wait, readTemps)
	return out
}

func readTemps() []entities.TempSensor {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ts, err := sensors.TemperaturesWithContext(ctx)
	var out []entities.TempSensor
	if err == nil || len(ts) > 0 {
		for _, t := range ts {
			if t.Temperature <= 0 || t.Temperature > 150 {
				continue
			}
			out = append(out, entities.TempSensor{Name: t.SensorKey, C: t.Temperature})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].C > out[j].C })
		if len(out) > maxTemps {
			out = out[:maxTemps]
		}
	}
	tempsState.Lock()
	tempsState.at = time.Now()
	tempsState.has = true
	if err == nil || len(ts) > 0 {
		tempsState.last = out
	}
	last := append([]entities.TempSensor(nil), tempsState.last...)
	tempsState.Unlock()
	return last
}

// appendGPUTemps adds each graphics device's own temperature. A desktop often
// publishes no ACPI zone, while nvidia-smi still reports the GPU.
func appendGPUTemps(ts []entities.TempSensor, gs []entities.GPUInfo) []entities.TempSensor {
	have := map[string]bool{}
	for _, t := range ts {
		have[t.Name] = true
	}
	for _, g := range gs {
		if g.TempC == nil || *g.TempC <= 0 || *g.TempC > 150 {
			continue
		}
		name := g.Name
		if name == "" {
			name = "GPU"
		}
		if have[name] {
			continue
		}
		have[name] = true
		ts = append(ts, entities.TempSensor{Name: name, C: *g.TempC})
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].C > ts[j].C })
	if len(ts) > maxTemps {
		ts = ts[:maxTemps]
	}
	return ts
}
