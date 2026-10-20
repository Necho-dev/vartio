package agent

import "testing"

func TestClampMem(t *testing.T) {
	used, comp, cache := clampMem(100, 40, 70, 50)
	if used != 60 || comp != 60 || cache != 40 {
		t.Fatalf("clamp got used=%d comp=%d cache=%d", used, comp, cache)
	}
	used, comp, cache = clampMem(64<<30, 54<<30, 4<<20, 32<<30)
	if used != 10<<30 || comp != 4<<20 || cache != 32<<30 {
		t.Fatalf("windows-shaped got used=%d comp=%d cache=%d", used, comp, cache)
	}
}
