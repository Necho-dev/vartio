package agent

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"vartio/internal/entities"
)

const (
	battCharging    = "charging"
	battFull        = "full"
	battDischarging = "discharging"
	battAC          = "ac"
)

// batteryState picks one label. Charging wins over a pack that is also full,
// so a laptop finishing its charge stays "charging".
func batteryState(charging, full, onAC bool) string {
	switch {
	case charging:
		return battCharging
	case full:
		return battFull
	case onAC:
		return battAC
	default:
		return battDischarging
	}
}

// batteryPercent is CurrentCapacity / MaxCapacity. Apple reports that pair
// either as a percentage or as milliamp-hours; the ratio is the charge either way.
func batteryPercent(cur, max int64) (float64, bool) {
	if max <= 0 || cur < 0 {
		return 0, false
	}
	pct := float64(cur) / float64(max) * 100
	if pct > 100 {
		pct = 100
	}
	return math.Round(pct*10) / 10, true
}

// batteryFromPowerStatus maps GetSystemPowerStatus. A missing pack
// (BatteryFlag bit 128, or percent 255) returns nil.
func batteryFromPowerStatus(ac, flag, pct byte) *entities.Battery {
	if flag&128 != 0 || pct > 100 {
		return nil
	}
	charging := flag&8 != 0
	onAC := ac == 1
	full := !charging && onAC && pct == 100
	return &entities.Battery{Percent: float64(pct), State: batteryState(charging, full, onAC)}
}

// readBatteryDir reads the first system battery under a power_supply directory.
// scope=Device is a peripheral (mouse, headset) and is skipped.
func readBatteryDir(root string) *entities.Battery {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if strings.TrimSpace(readSmall(filepath.Join(dir, "type"))) != "Battery" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(readSmall(filepath.Join(dir, "scope"))), "Device") {
			continue
		}
		b := batteryFromSysfs(readSmall(filepath.Join(dir, "capacity")), readSmall(filepath.Join(dir, "status")))
		if b != nil {
			return b
		}
	}
	return nil
}

func batteryFromSysfs(capacity, status string) *entities.Battery {
	pct, err := strconv.ParseFloat(strings.TrimSpace(capacity), 64)
	if err != nil || pct < 0 || pct > 100 {
		return nil
	}
	var charging, full, onAC bool
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "charging":
		charging = true
	case "full":
		full, onAC = true, true
	case "discharging":
	case "not charging":
		onAC = true
		full = pct >= 100
	default:
		onAC = true
	}
	return &entities.Battery{Percent: math.Round(pct*10) / 10, State: batteryState(charging, full, onAC)}
}

func readSmall(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64 {
		return ""
	}
	return string(b)
}
