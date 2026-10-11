package agent

import (
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"vartio/internal/entities"
)

var netPrev struct {
	mu sync.Mutex
	at time.Time
	rx uint64
	tx uint64
}

func collectHealth(watches []watchSpec) (entities.Health, error) {
	out := entities.Health{TimeUTC: time.Now().UTC().Format(time.RFC3339)}
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		out.CPU.Model = strings.TrimSpace(info[0].ModelName)
	}
	if n, err := cpu.Counts(true); err == nil {
		out.CPU.LogicalCPUs = n
	}
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		out.CPU.Pct = pct[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		out.Memory.TotalMB = int64(vm.Total / 1024 / 1024)
		out.Memory.AvailMB = int64(vm.Available / 1024 / 1024)
		out.Memory.Pct = vm.UsedPercent
	}
	if parts, err := disk.Partitions(false); err == nil {
		for _, p := range parts {
			u, err := disk.Usage(p.Mountpoint)
			if err != nil || u.Total == 0 {
				continue
			}
			out.Disks = append(out.Disks, entities.DiskInfo{
				Name:    p.Mountpoint,
				TotalGB: float64(u.Total) / 1e9,
				UsedGB:  float64(u.Used) / 1e9,
				Pct:     u.UsedPercent,
			})
		}
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if len(iface.Addrs) == 0 {
				continue
			}
			n := entities.NICInfo{Name: iface.Name, MAC: iface.HardwareAddr, Up: hasFlag(iface.Flags, "up")}
			for _, a := range iface.Addrs {
				if strings.Contains(a.Addr, ".") && !strings.HasPrefix(a.Addr, "127.") {
					n.IPv4 = strings.Split(a.Addr, "/")[0]
					break
				}
			}
			if n.IPv4 == "" {
				continue
			}
			out.NICs = append(out.NICs, n)
		}
	}
	out.Net = sampleNet()
	for _, w := range watches {
		out.Watches = append(out.Watches, evalWatch(w))
	}
	if hi, err := host.Info(); err == nil && out.CPU.Model == "" {
		out.CPU.Model = hi.Platform
	}
	return out, nil
}

func sampleNet() entities.NetRates {
	cs, err := net.IOCounters(false)
	if err != nil || len(cs) == 0 {
		return entities.NetRates{}
	}
	now := time.Now()
	rx, tx := cs[0].BytesRecv, cs[0].BytesSent
	netPrev.mu.Lock()
	defer netPrev.mu.Unlock()
	var rates entities.NetRates
	if !netPrev.at.IsZero() {
		dt := now.Sub(netPrev.at).Seconds()
		if dt > 0 {
			rates.RxMbps = float64(rx-netPrev.rx) * 8 / dt / 1e6
			rates.TxMbps = float64(tx-netPrev.tx) * 8 / dt / 1e6
		}
	}
	netPrev.at, netPrev.rx, netPrev.tx = now, rx, tx
	return rates
}

func listMACs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.HardwareAddr == "" || iface.HardwareAddr == "00:00:00:00:00:00" {
			continue
		}
		out = append(out, strings.ToLower(iface.HardwareAddr))
	}
	return out
}

func hasFlag(flags []string, want string) bool {
	want = strings.ToLower(want)
	for _, f := range flags {
		if strings.Contains(strings.ToLower(f), want) {
			return true
		}
	}
	return false
}

func evalWatch(w watchSpec) entities.Watch {
	switch w.Kind {
	case "process":
		ok, detail := procAlive(w.Name)
		return entities.Watch{Kind: w.Kind, Name: w.Name, OK: ok, Detail: detail}
	case "service":
		ok, detail := serviceAlive(w.Name)
		return entities.Watch{Kind: w.Kind, Name: w.Name, OK: ok, Detail: detail}
	default:
		return entities.Watch{Kind: w.Kind, Name: w.Name, OK: false, Detail: "unknown kind"}
	}
}
