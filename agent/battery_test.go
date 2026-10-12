package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBatteryPercent(t *testing.T) {
	pct, ok := batteryPercent(100, 100)
	if !ok || pct != 100 {
		t.Fatalf("percent scale %v %v", pct, ok)
	}
	pct, ok = batteryPercent(4842, 6075)
	if !ok || pct < 79 || pct > 80 {
		t.Fatalf("mAh scale %v", pct)
	}
	if _, ok := batteryPercent(1, 0); ok {
		t.Fatal("zero max")
	}
}

func TestBatteryFromPowerStatus(t *testing.T) {
	if b := batteryFromPowerStatus(1, 128, 100); b != nil {
		t.Fatalf("no battery %+v", b)
	}
	if b := batteryFromPowerStatus(1, 1, 255); b != nil {
		t.Fatalf("unknown percent %+v", b)
	}
	b := batteryFromPowerStatus(1, 8, 76)
	if b == nil || b.Percent != 76 || b.State != battCharging {
		t.Fatalf("charging %+v", b)
	}
	b = batteryFromPowerStatus(1, 1, 100)
	if b == nil || b.State != battFull {
		t.Fatalf("full %+v", b)
	}
	b = batteryFromPowerStatus(0, 2, 40)
	if b == nil || b.State != battDischarging {
		t.Fatalf("discharging %+v", b)
	}
	b = batteryFromPowerStatus(1, 0, 80)
	if b == nil || b.State != battAC {
		t.Fatalf("ac %+v", b)
	}
}

func TestBatteryFromSysfs(t *testing.T) {
	b := batteryFromSysfs("72\n", "Charging\n")
	if b == nil || b.Percent != 72 || b.State != battCharging {
		t.Fatalf("%+v", b)
	}
	b = batteryFromSysfs("100", "Not charging")
	if b == nil || b.State != battFull {
		t.Fatalf("not charging full %+v", b)
	}
	b = batteryFromSysfs("90", "Not charging")
	if b == nil || b.State != battAC {
		t.Fatalf("not charging %+v", b)
	}
	b = batteryFromSysfs("15", "Discharging")
	if b == nil || b.State != battDischarging {
		t.Fatalf("discharging %+v", b)
	}
	if batteryFromSysfs("", "Charging") != nil {
		t.Fatal("missing capacity")
	}
}

func TestReadBatteryDir(t *testing.T) {
	dir := t.TempDir()
	writePS(t, dir, "hid", "Battery", "Device", "40", "Discharging")
	writePS(t, dir, "AC", "Mains", "", "", "")
	writePS(t, dir, "BAT0", "Battery", "System", "72", "Charging")
	b := readBatteryDir(dir)
	if b == nil || b.Percent != 72 || b.State != battCharging {
		t.Fatalf("%+v", b)
	}
	if readBatteryDir(filepath.Join(dir, "missing")) != nil {
		t.Fatal("missing dir")
	}
}

func writePS(t *testing.T, root, name, kind, scope, capacity, status string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(file, body string) {
		t.Helper()
		if body == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("type", kind)
	put("scope", scope)
	put("capacity", capacity)
	put("status", status)
}
