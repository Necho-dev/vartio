package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedHungCollectorDoesNotStallSamples(t *testing.T) {
	old := stageWait
	stageWait = 50 * time.Millisecond
	defer func() { stageWait = old }()

	var r limitedRead[int]
	if v := bounded(&r, func(context.Context) int { return 3 })(); v != 3 {
		t.Fatalf("healthy sample = %d", v)
	}
	var calls atomic.Int32
	release := make(chan struct{})
	hung := func(context.Context) int {
		calls.Add(1)
		<-release
		return 7
	}
	if v := bounded(&r, hung)(); v != 3 {
		t.Fatalf("sample when the collector hangs = %d, want last value 3", v)
	}
	start := time.Now()
	for range 5 {
		if v := bounded(&r, hung)(); v != 3 {
			t.Fatalf("sample while hung = %d, want 3", v)
		}
	}
	if d := time.Since(start); d > 30*time.Millisecond {
		t.Fatalf("samples behind a hung collector took %s", d)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("hung collector started %d times, want 1", n)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		v := bounded(&r, func(context.Context) int { return 9 })()
		if v == 9 || v == 7 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("collector never recovered, last %d", v)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCollectSnapshotIsEncodable(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the host")
	}
	for range 2 {
		m := collectSnapshot()
		if _, err := encode("metrics", m); err != nil {
			t.Fatal(err)
		}
		if m.MemTotal == 0 || len(m.Disks) == 0 {
			t.Fatalf("snapshot missing basics: mem %d disks %d", m.MemTotal, len(m.Disks))
		}
	}
}
