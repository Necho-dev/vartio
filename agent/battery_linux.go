//go:build linux

package agent

import "vartio/internal/entities"

func readBattery() *entities.Battery {
	return readBatteryDir("/sys/class/power_supply")
}
