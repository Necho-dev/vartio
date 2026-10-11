package systems

import (
	"strings"
	"testing"
	"vartio/internal/entities"
)

func TestDiffReportsChanges(t *testing.T) {
	pi := entities.SysInfo{HostName: "a", Platform: "ubuntu", PlatformVersion: "22.04", Kernel: "5.15", Threads: 4, BootTime: 1000}
	ni := entities.SysInfo{HostName: "b", Platform: "ubuntu", PlatformVersion: "24.04", Kernel: "6.8", Threads: 8, BootTime: 5000}
	ps := &Sample{MemTotal: 8 << 30,
		Disks: []entities.DiskUsage{{Mount: "/", Total: 50 << 30}, {Mount: "/old", Total: 1 << 30}},
		NICs:  []entities.NICInfo{{Name: "eth0", IPv4: "10.0.0.2"}, {Name: "veth1", IPv4: "172.17.0.1"}}}
	ns := &Sample{MemTotal: 16 << 30,
		Disks: []entities.DiskUsage{{Mount: "/", Total: 100 << 30}, {Mount: "/data", Total: 10 << 30}},
		NICs:  []entities.NICInfo{{Name: "eth0", IPv4: "10.0.0.3"}}}
	got := map[string]string{}
	for _, e := range diff(pi, ps, ni, ns) {
		got[e.Kind] = e.Detail
	}
	for _, k := range []string{"reboot", "hostname", "os", "kernel", "cpu", "memory", "disk", "nic"} {
		if got[k] == "" {
			t.Errorf("missing %s event, got %v", k, got)
		}
	}
	if !strings.Contains(got["disk"], "新增 /data") || !strings.Contains(got["disk"], "移除 /old") {
		t.Errorf("disk detail %q", got["disk"])
	}
	if strings.Contains(got["nic"], "veth") {
		t.Errorf("transient NIC leaked: %q", got["nic"])
	}
}

func TestDiffQuietOnSteadyState(t *testing.T) {
	i := entities.SysInfo{HostName: "a", Platform: "ubuntu", PlatformVersion: "22.04", Threads: 4, BootTime: 1000}
	j := i
	j.BootTime = 1001
	s := &Sample{MemTotal: 8 << 30, Disks: []entities.DiskUsage{{Mount: "/", Total: 50 << 30}}}
	t2 := *s
	t2.MemTotal += 1 << 20
	if ev := diff(i, s, j, &t2); len(ev) != 0 {
		t.Fatalf("want no events, got %+v", ev)
	}
	if ev := diff(entities.SysInfo{}, nil, i, s); len(ev) != 0 {
		t.Fatalf("first sample must be quiet, got %+v", ev)
	}
}

func TestAdoptLegacyAndFingerprintDrift(t *testing.T) {
	m := &Monitor{systems: map[string]*System{
		"bios-1": {ID: "bios-1", BiosUUID: "bios-1"},
		"fp-old": {ID: "fp-old", Device: &entities.DeviceFacts{MID: "m", HW: "h", MAC: "x"}},
		"fp-on":  {ID: "fp-on", Online: true, Device: &entities.DeviceFacts{MID: "q", HW: "w", MAC: "e"}},
	}}
	if s, why := m.adoptLocked(entities.SessionInfo{DeviceID: "fp-new", BiosUUID: "bios-1"}); s == nil || s.ID != "bios-1" || why == "" {
		t.Fatalf("legacy row not adopted: %v %q", s, why)
	}
	if s, why := m.adoptLocked(entities.SessionInfo{DeviceID: "fp-2", BiosUUID: "z",
		Device: &entities.DeviceFacts{MID: "m", HW: "h", MAC: "y"}}); s == nil || s.ID != "fp-old" || !strings.Contains(why, "MAC") {
		t.Fatalf("drifted fingerprint not adopted: %v %q", s, why)
	}
	if s, _ := m.adoptLocked(entities.SessionInfo{DeviceID: "fp-3", BiosUUID: "z",
		Device: &entities.DeviceFacts{MID: "q", HW: "w", MAC: "r"}}); s == nil || !s.Online {
		t.Fatalf("online candidate is returned for the liveness check, got %v", s)
	}
	if s, _ := m.adoptLocked(entities.SessionInfo{DeviceID: "fp-4", BiosUUID: "z",
		Device: &entities.DeviceFacts{MID: "other", HW: "h", MAC: "y"}}); s != nil {
		t.Fatalf("one matching input is not enough, got %v", s.ID)
	}
}
