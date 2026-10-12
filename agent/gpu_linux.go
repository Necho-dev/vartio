//go:build linux

package agent

import (
	"context"

	"vartio/internal/entities"
)

func platformGPUs(ctx context.Context) []entities.GPUInfo {
	nv := nvidiaGPUs(ctx)
	drm := drmGPUs("/sys/class/drm")
	if len(nv) == 0 {
		return drm
	}
	out := append([]entities.GPUInfo{}, nv...)
	for _, g := range drm {
		if g.Vendor == "nvidia" {
			continue
		}
		out = append(out, g)
	}
	return out
}
