package main

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
	"github.com/shirou/gopsutil/v4/load"
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
	if pct, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pct) > 0 {
		out.CPUPct = pct[0]
	}
	if runtime.GOOS != "windows" {
		if l, err := load.AvgWithContext(ctx); err == nil {
			out.Load = [3]float64{l.Load1, l.Load5, l.Load15}
		}
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
	out.NetRx, out.NetTx = netIO(ctx)
	out.NICs = nicList(ctx)
	out.Temps = temps(ctx)
	if pids, err := process.PidsWithContext(ctx); err == nil {
		out.Procs = len(pids)
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
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, guest.DiskUsage{Mount: p.Mountpoint, FS: p.Fstype, Total: u.Total, Used: u.Used})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out, pickRoot(out)
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
	for _, w := range want {
		for _, d := range disks {
			if strings.EqualFold(d.Mount, w) {
				return d.Mount
			}
		}
	}
	if len(disks) > 0 {
		return disks[0].Mount
	}
	return ""
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

func netIO(ctx context.Context) (rx, tx uint64) {
	cs, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return 0, 0
	}
	for _, c := range cs {
		if skipNIC(c.Name) {
			continue
		}
		rx += c.BytesRecv
		tx += c.BytesSent
	}
	return rx, tx
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
