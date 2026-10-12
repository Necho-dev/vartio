//go:build windows

package agent

import (
	"unsafe"

	"golang.org/x/sys/windows"

	"vartio/internal/entities"
)

var getSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func readBattery() *entities.Battery {
	var st systemPowerStatus
	r, _, _ := getSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return nil
	}
	return batteryFromPowerStatus(st.ACLineStatus, st.BatteryFlag, st.BatteryLifePercent)
}
