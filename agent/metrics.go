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

	"vartio/internal/entities"
)

const maxTemps = 12

// identityEvery is how often the native identity read (platform, kernel, CPU,
// model) runs again. Hostname is read on every sample; these fields change rarely.
const identityEvery = 10 * time.Minute

var identityState struct {
	sync.Mutex
	at time.Time
	v  entities.SysInfo
}

// staticInfo returns the cached identity plus the current hostname. It does not
// call sysctl; the sample path refreshes the cache when it is due.
func staticInfo() entities.SysInfo {
	return withLiveHost(cachedIdentity())
}

// refreshIdentity rereads native identity when the cache is older than
// identityEvery. identityRead keeps two refreshes from running at once.
func refreshIdentity() entities.SysInfo {
	identityState.Lock()
	due := identityStale(identityState.at, time.Now())
	identityState.Unlock()
	if due {
		v := readStaticInfo()
		if identitySmashed(v) {
			v = readStaticInfo()
		}
		identityState.Lock()
		identityState.v = scrubSys(v)
		identityState.at = time.Now()
		identityState.Unlock()
	}
	return withLiveHost(cachedIdentity())
}

func cachedIdentity() entities.SysInfo {
	identityState.Lock()
	defer identityState.Unlock()
	return identityState.v
}

func identityStale(at, now time.Time) bool {
	return at.IsZero() || now.Sub(at) >= identityEvery
}

func withLiveHost(v entities.SysInfo) entities.SysInfo {
	v = scrubSys(v)
	v.HostName = hostname()
	if bt, err := host.BootTime(); err == nil {
		v.BootTime = int64(bt)
	}
	return v
}

func scrubSys(v entities.SysInfo) entities.SysInfo {
	v.HostName = scrubNUL(v.HostName)
	v.Platform = scrubNUL(v.Platform)
	v.PlatformVersion = scrubNUL(v.PlatformVersion)
	v.Kernel = scrubNUL(v.Kernel)
	v.Arch = scrubNUL(v.Arch)
	v.CPUModel = scrubNUL(v.CPUModel)
	v.Vendor = scrubNUL(v.Vendor)
	v.Product = scrubNUL(v.Product)
	v.Virt = scrubNUL(v.Virt)
	return v
}

func identitySmashed(v entities.SysInfo) bool {
	for _, s := range []string{v.Platform, v.PlatformVersion, v.Kernel, v.CPUModel, v.Product, v.Vendor} {
		if strings.IndexByte(s, 0) >= 0 {
			return true
		}
	}
	return false
}

func scrubNUL(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

func readStaticInfo() entities.SysInfo {
	v := entities.SysInfo{HostName: hostname(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if hi, err := host.Info(); err == nil {
		v.Platform = strings.Clone(hi.Platform)
		v.PlatformVersion = strings.Clone(hi.PlatformVersion)
		v.Kernel = strings.Clone(hi.KernelVersion)
		if hi.KernelArch != "" {
			v.Arch = strings.Clone(hi.KernelArch)
		}
	}
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		v.CPUModel = strings.Clone(strings.TrimSpace(info[0].ModelName))
	}
	vendor, product, virt := virtualization()
	v.Vendor, v.Product, v.Virt = strings.Clone(vendor), strings.Clone(product), strings.Clone(virt)
	v.Cores, _ = cpu.Counts(false)
	v.Threads, _ = cpu.Counts(true)
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

// stageWait is how long one sample waits for a collector, and stageTimeout the
// deadline the collector itself gets. Several native reads have no timeout of
// their own: statfs on a dead network mount, PDH collection on Windows, sysfs
// files of a stuck driver. A collector that overruns keeps running alone while
// later samples reuse its last value, so one hung read costs that metric only.
var stageWait = 3 * time.Second

const stageTimeout = 10 * time.Second

type memStage struct {
	load      [3]float64
	memTotal  uint64
	memUsed   uint64
	swapTotal uint64
	swapUsed  uint64
	procs     int
}

type diskStage struct {
	disks       []entities.DiskUsage
	root        string
	read, write uint64
}

type netStage struct {
	rx, tx uint64
	pkts   *entities.NetPackets
	tcp    *entities.TCPStats
	nics   []entities.NICInfo
}

var (
	memRead      limitedRead[memStage]
	diskRead     limitedRead[diskStage]
	netRead      limitedRead[netStage]
	tempsRead    limitedRead[[]entities.TempSensor]
	gpusRead     limitedRead[[]entities.GPUInfo]
	batteryRead  limitedRead[*entities.Battery]
	dockerRead   limitedRead[*entities.DockerInfo]
	identityRead limitedRead[entities.SysInfo]
)

// bounded starts fn under r and returns a function that yields its result, or the
// previous one when fn is still running after stageWait.
func bounded[T any](r *limitedRead[T], fn func(context.Context) T) func() T {
	ch := make(chan T, 1)
	go func() {
		v, _ := r.get(stageWait, func() T {
			ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
			defer cancel()
			return fn(ctx)
		})
		ch <- v
	}()
	return func() T { return <-ch }
}

func readMem(ctx context.Context) memStage {
	var s memStage
	if l, ok := loadAvg(ctx); ok {
		s.load = l
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		s.memTotal = vm.Total
		s.memUsed = vm.Total - vm.Available
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		s.swapTotal, s.swapUsed = sw.Total, sw.Used
	}
	if pids, err := process.PidsWithContext(ctx); err == nil {
		s.procs = len(pids)
	}
	return s
}

func readDisks(ctx context.Context) diskStage {
	var s diskStage
	s.disks, s.root = diskUsages(ctx)
	s.read, s.write = diskIO(ctx)
	return s
}

func readNet(ctx context.Context) netStage {
	var s netStage
	s.rx, s.tx, s.pkts = netIO(ctx)
	if t, err := tcpStats(ctx); err == nil {
		s.tcp = t
	}
	s.nics = nicList(ctx)
	return s
}

func collectSnapshot() entities.Metrics {
	out := entities.Metrics{TimeMs: time.Now().UnixMilli()}
	memS := bounded(&memRead, readMem)
	diskS := bounded(&diskRead, readDisks)
	netS := bounded(&netRead, readNet)
	tempsS := bounded(&tempsRead, temps)
	gpusS := bounded(&gpusRead, gpus)
	batteryS := bounded(&batteryRead, func(context.Context) *entities.Battery { return readBattery() })
	identityS := bounded(&identityRead, func(context.Context) entities.SysInfo { return refreshIdentity() })
	var dockerS func() *entities.DockerInfo
	if docker() != nil {
		dockerS = bounded(&dockerRead, func(ctx context.Context) *entities.DockerInfo {
			c, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
			defer cancel()
			return dockerMetrics(c)
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), stageWait)
	out.CPUPct, out.CPUUserPct, out.CPUSystemPct, out.CPUWaitPct, out.Cores = sampleCPU(ctx)
	cancel()

	m := memS()
	out.Load, out.MemTotal, out.MemUsed, out.SwapTotal, out.SwapUsed, out.Procs = m.load, m.memTotal, m.memUsed, m.swapTotal, m.swapUsed, m.procs
	d := diskS()
	out.Disks, out.RootMount, out.DiskRead, out.DiskWrite = d.disks, d.root, d.read, d.write
	n := netS()
	out.NetRx, out.NetTx, out.NetPkts, out.TCP, out.NICs = n.rx, n.tx, n.pkts, n.tcp, n.nics
	out.GPUs = gpusS()
	out.Battery = batteryS()
	// A reused reading is shared with the cache; appendGPUTemps sorts in place.
	out.Temps = appendGPUTemps(append([]entities.TempSensor(nil), tempsS()...), out.GPUs)
	if dockerS != nil {
		out.Docker = dockerS()
	}
	out.Info = withLiveHost(identityS())
	entities.Finite(&out)
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

// temps is best effort and cached for a minute, including an empty reading.
// Windows reads ACPI thermal zones, which a desktop often does not publish, so
// an empty read is retried next minute instead of turning the sensor off.
// macOS reads IOHID sensors (CPU die, GPU, NAND, battery); GUEST_TEMPS=0 turns
// that off. GPU core temperature is added separately.
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
	if ctx.Err() != nil {
		tempsState.Lock()
		last := append([]entities.TempSensor(nil), tempsState.last...)
		tempsState.Unlock()
		return last
	}
	return readTemps()
}

func readTemps() []entities.TempSensor {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ts, err := sensorTemps(ctx)
	var out []entities.TempSensor
	if err == nil || len(ts) > 0 {
		for _, t := range ts {
			// Written so NaN fails too; encoding/json cannot encode it.
			if !(t.Temperature > 0 && t.Temperature <= 150) {
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
