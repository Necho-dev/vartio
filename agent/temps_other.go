//go:build !(darwin && arm64)

package agent

import (
	"context"

	"github.com/shirou/gopsutil/v4/sensors"
)

func sensorTemps(ctx context.Context) ([]sensors.TemperatureStat, error) {
	return sensors.TemperaturesWithContext(ctx)
}
