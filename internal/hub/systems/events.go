package monitor

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"govmomi-wmks/internal/guest"
)

// rebootSlack absorbs boot-time jitter: agents derive it from uptime, which drifts
// by a second or two between samples.
const rebootSlack = 2 * time.Minute

// diff compares two consecutive samples and reports reboots and configuration
// changes. The previous side may be empty (first sample after hub start for a
// system with no stored history), in which case nothing is reported.
func diff(pi guest.SysInfo, ps *Sample, ni guest.SysInfo, ns *Sample) []Event {
	var out []Event
	add := func(cat, kind, detail string) {
		out = append(out, Event{Kind: kind, Category: cat, Level: LevelInfo, Detail: detail})
	}
	if pi.BootTime > 0 && ni.BootTime-pi.BootTime > int64(rebootSlack/time.Second) {
		add(CatLifecycle, "reboot", "启动于 "+time.Unix(ni.BootTime, 0).Format("2006-01-02 15:04:05"))
	}
	if changed(pi.HostName, ni.HostName) {
		add(CatChange, "hostname", pi.HostName+" → "+ni.HostName)
	}
	if po, no := osLine(pi), osLine(ni); changed(po, no) {
		add(CatChange, "os", po+" → "+no)
	}
	if changed(pi.Kernel, ni.Kernel) {
		add(CatChange, "kernel", pi.Kernel+" → "+ni.Kernel)
	}
	if pi.Threads > 0 && ni.Threads > 0 && pi.Threads != ni.Threads {
		add(CatChange, "cpu", fmt.Sprintf("%d → %d 线程", pi.Threads, ni.Threads))
	} else if changed(pi.CPUModel, ni.CPUModel) {
		add(CatChange, "cpu", pi.CPUModel+" → "+ni.CPUModel)
	}
	if ps == nil {
		return out
	}
	if moved(ps.MemTotal, ns.MemTotal) {
		add(CatChange, "memory", humanBytes(ps.MemTotal)+" → "+humanBytes(ns.MemTotal))
	}
	if d := diskChanges(ps.Disks, ns.Disks); d != "" {
		add(CatChange, "disk", d)
	}
	if d := nicChanges(ps.NICs, ns.NICs); d != "" {
		add(CatChange, "nic", d)
	}
	return append(out, containerEvents(ps.Docker, ns.Docker)...)
}

// containerEvents reports containers that stopped, started or changed health,
// one event per kind. An engine that did not answer either time says nothing.
func containerEvents(prev, cur *DockerSample) []Event {
	if prev == nil || cur == nil || prev.Error != "" || cur.Error != "" {
		return nil
	}
	was := make(map[string]ContainerSample, len(prev.Containers))
	for _, c := range prev.Containers {
		was[c.Name] = c
	}
	var stopped, started, sick, well []string
	seen := map[string]bool{}
	for _, c := range cur.Containers {
		seen[c.Name] = true
		p, ok := was[c.Name]
		switch {
		case c.State == "running" && (!ok || p.State != "running"):
			started = append(started, c.Name)
		case c.State != "running" && ok && p.State == "running":
			stopped = append(stopped, c.Name+"（"+c.Status+"）")
		}
		if ok && c.Health != p.Health {
			switch {
			case c.Health == "unhealthy":
				sick = append(sick, c.Name)
			case c.Health == "healthy" && p.Health == "unhealthy":
				well = append(well, c.Name)
			}
		}
	}
	// A full list cannot tell a removed container from one cut off by MaxContainers.
	if len(cur.Containers) < guest.MaxContainers {
		for _, c := range prev.Containers {
			if !seen[c.Name] && c.State == "running" {
				stopped = append(stopped, c.Name+"（已删除）")
			}
		}
	}
	var out []Event
	add := func(kind, level string, names []string) {
		if len(names) == 0 {
			return
		}
		sort.Strings(names)
		detail := strings.Join(names[:min(len(names), 10)], "、")
		if len(names) > 10 {
			detail += fmt.Sprintf(" 等 %d 个", len(names))
		}
		out = append(out, Event{Kind: kind, Category: CatLifecycle, Level: level, Detail: detail})
	}
	add("container.stop", LevelWarn, stopped)
	add("container.start", LevelInfo, started)
	add("container.unhealthy", LevelWarn, sick)
	add("container.healthy", LevelInfo, well)
	return out
}

func changed(a, b string) bool { return a != "" && b != "" && a != b }

func osLine(i guest.SysInfo) string {
	return strings.TrimSpace(i.Platform + " " + i.PlatformVersion)
}

// moved ignores sub-2% wobble (reserved memory, filesystem metadata).
func moved(a, b uint64) bool {
	if a == 0 || b == 0 {
		return false
	}
	return math.Abs(float64(b)-float64(a))/float64(a) > 0.02
}

func diskChanges(prev, cur []guest.DiskUsage) string {
	pm := map[string]uint64{}
	for _, d := range prev {
		pm[d.Mount] = d.Total
	}
	var parts []string
	seen := map[string]bool{}
	for _, d := range cur {
		seen[d.Mount] = true
		old, ok := pm[d.Mount]
		switch {
		case !ok:
			parts = append(parts, "新增 "+d.Mount+" ("+humanBytes(d.Total)+")")
		case moved(old, d.Total):
			parts = append(parts, d.Mount+" "+humanBytes(old)+" → "+humanBytes(d.Total))
		}
	}
	for _, d := range prev {
		if !seen[d.Mount] {
			parts = append(parts, "移除 "+d.Mount)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "；")
}

// Container and overlay interfaces come and go with workloads; only addressed,
// non-virtual NICs count as configuration.
var transientNIC = []string{"veth", "docker", "br-", "cali", "flannel", "cni", "vnet", "virbr", "kube", "tun", "tap", "utun", "zt", "tailscale", "wg"}

func stableNICs(list []guest.NICInfo) map[string]guest.NICInfo {
	out := map[string]guest.NICInfo{}
	for _, n := range list {
		if n.IPv4 == "" || strings.HasPrefix(n.IPv4, "127.") {
			continue
		}
		name := strings.ToLower(n.Name)
		skip := false
		for _, p := range transientNIC {
			if strings.HasPrefix(name, p) {
				skip = true
				break
			}
		}
		if !skip {
			out[n.Name] = n
		}
	}
	return out
}

func nicChanges(prev, cur []guest.NICInfo) string {
	pm, cm := stableNICs(prev), stableNICs(cur)
	var parts []string
	for name, n := range cm {
		o, ok := pm[name]
		switch {
		case !ok:
			parts = append(parts, "新增 "+name+" "+n.IPv4)
		case o.IPv4 != n.IPv4:
			parts = append(parts, name+" "+o.IPv4+" → "+n.IPv4)
		case o.MAC != "" && n.MAC != "" && o.MAC != n.MAC:
			parts = append(parts, name+" MAC "+o.MAC+" → "+n.MAC)
		}
	}
	for name, o := range pm {
		if _, ok := cm[name]; !ok {
			parts = append(parts, "移除 "+name+" "+o.IPv4)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "；")
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n), 0
	for v >= unit && i < 5 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", v, []string{"B", "KB", "MB", "GB", "TB", "PB"}[i])
}
