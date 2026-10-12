//go:build darwin

package agent

import (
	"context"
	"testing"
)

func TestAppleGPULive(t *testing.T) {
	gs := appleGPUs(context.Background())
	if len(gs) == 0 {
		t.Skip("no IOAccelerator performance statistics")
	}
	g := gs[0]
	if g.Name == "" || g.UtilPct < 0 || g.UtilPct > 100 {
		t.Fatalf("%+v", g)
	}
	if g.RenderPct == nil || g.TilerPct == nil {
		t.Fatalf("expected renderer and tiler: %+v", g)
	}
}
