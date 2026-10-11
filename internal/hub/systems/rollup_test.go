package systems

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
	"vartio/internal/entities"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPickSource(t *testing.T) {
	now, week := time.Now(), 7*24*time.Hour
	for _, c := range []struct {
		span     time.Duration
		retain   time.Duration
		src      string
		wantStep time.Duration
	}{
		{time.Hour, week, "raw", 15 * time.Second},
		{24 * time.Hour, week, "raw", 6 * time.Minute},
		{24 * time.Hour, 12 * time.Hour, "5m", 10 * time.Minute},
		{7 * 24 * time.Hour, week, "5m", 45 * time.Minute},
		{30 * 24 * time.Hour, week, "1h", 3 * time.Hour},
	} {
		src, step := PickSource(now.Add(-c.span), stepFor(c.span, 10*time.Second), c.retain)
		if src.Name != c.src || step != c.wantStep {
			t.Errorf("span %s retain %s: %s %s, want %s %s", c.span, c.retain, src.Name, step, c.src, c.wantStep)
		}
	}
}

// TestRollupMatchesRaw needs PostgreSQL: PROBE_HUB_TEST_DSN=postgres://... (a
// throwaway schema is created and dropped).
func TestRollupMatchesRaw(t *testing.T) {
	dsn := os.Getenv("PROBE_HUB_TEST_DSN")
	if dsn == "" {
		t.Skip("PROBE_HUB_TEST_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("probe_rollup_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	st, err := OpenStore(ctx, dsn+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	start := time.Now().Add(-2 * time.Hour).Truncate(time.Hour)
	if err := st.Connect(ctx, System{ID: "d1", HostName: "h", ConnectedAt: start}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 720; i++ {
		cpu, rx := float64(i%10*10), uint64(1000)
		smp := Sample{TS: start.Add(time.Duration(i) * 10 * time.Second), CPUPct: cpu, MemUsed: uint64(i) << 20, MemTotal: 8 << 30,
			NetRxBps: float64(i % 7), NetRxBytes: rx, Load: [3]float64{1, 2, 3}, Procs: 100 + i%3}
		smp.Docker = &DockerSample{Containers: []ContainerSample{{Name: "web", Stats: true, CPUPct: &cpu, MemUsed: 64 << 20, MemLimit: 1 << 30}}}
		smp.Disks = []entities.DiskUsage{{Mount: "/", Used: uint64(i%2) * 1000}}
		if err := st.Record(ctx, "d1", entities.SysInfo{}, smp); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // idempotent
		if err := st.Rollup(ctx, start, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	step := time.Hour
	raw, err := st.Series(ctx, "d1", SourceRaw, start, step, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []Source{Source5m, Source1h} {
		got, err := st.Series(ctx, "d1", src, start, step, 10*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", src.Name, err)
		}
		if len(got.T) != len(raw.T) || len(got.T) != 2 {
			t.Fatalf("%s buckets %v vs raw %v", src.Name, got.T, raw.T)
		}
		for i := range raw.T {
			for name, pair := range map[string][2]float64{
				"cpu": {raw.CPU[i], got.CPU[i]}, "cpuMax": {raw.CPUMax[i], got.CPUMax[i]}, "mem": {raw.Mem[i], got.Mem[i]},
				"rxMax": {raw.NetRxMax[i], got.NetRxMax[i]}, "rxBytes": {raw.NetRxBytes[i], got.NetRxBytes[i]}, "procs": {raw.Procs[i], got.Procs[i]},
			} {
				if math.Abs(pair[0]-pair[1]) > 1e-6*math.Max(1, math.Abs(pair[0])) {
					t.Errorf("%s bucket %d %s: raw %v rollup %v", src.Name, i, name, pair[0], pair[1])
				}
			}
			if p := got.DiskParts["/"]; p == nil || p[i] == nil || *p[i] != 500 {
				t.Errorf("%s bucket %d disk parts %v", src.Name, i, got.DiskParts)
			}
		}
		cs, err := st.ContainerSeries(ctx, "d1", src, start, step, 8)
		if err != nil || len(cs.Containers) != 1 || *cs.Containers[0].CPU[0] != 45 || cs.Containers[0].MemLimit != 1<<30 {
			t.Fatalf("%s containers %+v %v", src.Name, cs, err)
		}
	}
	if err := st.PruneRollups(ctx, start.Add(time.Hour), start); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Series(ctx, "d1", Source5m, start, step, 10*time.Second); len(got.T) != 1 {
		t.Fatalf("pruned 5m buckets %v", got.T)
	}
	if last, err := st.LastRollup(ctx); err != nil || !last.Equal(start.Add(2*time.Hour-5*time.Minute)) {
		t.Fatalf("last rollup %v %v", last, err)
	}
}
