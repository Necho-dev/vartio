package main

import (
	"context"
	"fmt"
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

	"govmomi-wmks/internal/guest"
)

const maxTemps = 12

var sysInfoOnce struct {
	sync.Once
	v guest.SysInfo
}

func staticInfo() guest.SysInfo {
	sysInfoOnce.Do(func() {
		v := guest.SysInfo{HostName: hostname(), OS: runtime.GOOS, Arch: runtime.GOARCH}
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

func collectMetrics() guest.Metrics {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out := guest.Metrics{TimeMs: time.Now().UnixMilli(), Info: staticInfo()}
	var containers chan *guest.DockerInfo
	if docker() != nil {
		containers = make(chan *guest.DockerInfo, 1)
		go func() {
			c, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
			defer cancel()
			containers <- dockerMetrics(c)
		}()
	}
	if pct, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pct) > 0 {
		out.CPUPct = pct[0]
	}
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
	if pids, err := process.PidsWithContext(ctx); err == nil {
		out.Procs = len(pids)
	}
	if containers != nil {
		out.Docker = <-containers
	}
	return out
}

func diskUsages(ctx context.Context) ([]guest.DiskUsage, string) {
	parts, _ := disk.PartitionsWithContext(ctx, false)
	seen := map[string]bool{}
	var out []guest.DiskUsage
	for _, p := range parts {
		if skipMount(p) || seen[p.Device] {
			continue
		}
		u, err := disk.UsageWithContext(ctx, hostPath(p.Mountpoint))
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, guest.DiskUsage{Mount: p.Mountpoint, FS: p.Fstype, Total: u.Total, Used: u.Used})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	root := pickRoot(out)
	return dedupePools(out, root), root
}

// dedupePools keeps one mount per storage pool. APFS volumes in one container
// (e.g. macOS "/" and "/System/Volumes/Data") all report the container's size
// and free space, so identical fs/total/used means the same space; the root
// mount wins.
func dedupePools(disks []guest.DiskUsage, root string) []guest.DiskUsage {
	key := func(d guest.DiskUsage) string { return fmt.Sprintf("%s|%d|%d", d.FS, d.Total, d.Used) }
	keep := map[string]string{}
	for _, d := range disks {
		if k := key(d); keep[k] == "" || d.Mount == root {
			keep[k] = d.Mount
		}
	}
	out := disks[:0]
	for _, d := range disks {
		if keep[key(d)] == d.Mount {
			out = append(out, d)
		}
	}
	return out
}

func pickRoot(disks []guest.DiskUsage) string {
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
	return guest.CapacityMount(disks, pref)
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

func netIO(ctx context.Context) (rx, tx uint64, pk *guest.NetPackets) {
	cs, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return 0, 0, nil
	}
	pk = &guest.NetPackets{}
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

func nicList(ctx context.Context) []guest.NICInfo {
	ifaces, err := net.InterfacesWithContext(ctx)
	if err != nil {
		return nil
	}
	var out []guest.NICInfo
	for _, iface := range ifaces {
		if skipNIC(iface.Name) {
			continue
		}
		n := guest.NICInfo{Name: iface.Name, MAC: iface.HardwareAddr, Up: hasFlag(iface.Flags, "up")}
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
	at   time.Time
	last []guest.TempSensor
}

// temps is best effort and cached for a minute. Windows reads WMI thermal zones (often
// admin-only or empty), so the first failure disables it. On macOS gopsutil reaches IOKit
// through purego and segfaults the process, so it is opt-in via GUEST_TEMPS=1.
func temps(ctx context.Context) []guest.TempSensor {
	tempsState.Lock()
	defer tempsState.Unlock()
	if tempsState.off {
		return nil
	}
	if runtime.GOOS == "darwin" && os.Getenv("GUEST_TEMPS") != "1" {
		tempsState.off = true
		return nil
	}
	if time.Since(tempsState.at) < time.Minute {
		return tempsState.last
	}
	tempsState.at = time.Now()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ts, err := sensors.TemperaturesWithContext(ctx)
	if err != nil && len(ts) == 0 {
		if runtime.GOOS == "windows" {
			tempsState.off = true
		}
		tempsState.last = nil
		return nil
	}
	var out []guest.TempSensor
	for _, t := range ts {
		if t.Temperature <= 0 || t.Temperature > 150 {
			continue
		}
		out = append(out, guest.TempSensor{Name: t.SensorKey, C: t.Temperature})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].C > out[j].C })
	if len(out) > maxTemps {
		out = out[:maxTemps]
	}
	tempsState.last = out
	return out
}
