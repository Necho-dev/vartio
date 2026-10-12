//go:build !darwin && !linux && !windows

package agent

import (
	"context"

	"vartio/internal/entities"
)

func platformGPUs(context.Context) []entities.GPUInfo { return nil }
