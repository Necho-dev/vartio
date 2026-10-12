package agent

import (
	"os"
	"path/filepath"
	"testing"

	"vartio/internal/entities"
)

const ioregFixture = `
+-o AGXAcceleratorG13X  <class AGXAcceleratorG13X, id 0x100000a06, registered, matched, active, busy 0 (2670 ms), retain 68>
  | {
  |   "PerformanceStatistics" = {"In use system memory (driver)"=0,"Alloc system memory"=4534697984,"Tiler Utilization %"=3,"Renderer Utilization %"=21,"Device Utilization %"=23,"In use system memory"=620167168}
  |   "model" = "Apple M1 Pro"
  |   "gpu-core-count" = 14
  |   "IOClass" = "AGXAcceleratorG13X"
  | }
`

func TestParseIOReg(t *testing.T) {
	gs := parseIOReg(ioregFixture)
	if len(gs) != 1 {
		t.Fatalf("got %d gpus", len(gs))
	}
	g := gs[0]
	if g.Name != "Apple M1 Pro" || g.Vendor != "apple" || g.Cores != 14 || g.UtilPct != 23 {
		t.Fatalf("%+v", g)
	}
	if g.RenderPct == nil || *g.RenderPct != 21 || g.TilerPct == nil || *g.TilerPct != 3 {
		t.Fatalf("split %+v %+v", g.RenderPct, g.TilerPct)
	}
	if g.MemUsed != 620167168 || g.MemTotal != 0 {
		t.Fatalf("mem used=%d total=%d", g.MemUsed, g.MemTotal)
	}
}

func TestParseIORegIgnoresNodesWithoutUtil(t *testing.T) {
	text := `
+-o Something  <class IOAccelerator, id 0x1>
  | {
  |   "PerformanceStatistics" = {"Alloc system memory"=10}
  | }
`
	if gs := parseIOReg(text); len(gs) != 0 {
		t.Fatalf("%+v", gs)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	out := "NVIDIA GeForce RTX 5070 Ti, 61, 7168, 16384, 57, 175.40, 2587\n" +
		"NVIDIA GeForce RTX 4060, 0, 100, 8192, [N/A], [Not Supported], 210\n"
	gs := parseNvidiaSMI(out)
	if len(gs) != 2 {
		t.Fatalf("got %d", len(gs))
	}
	if gs[0].Vendor != "nvidia" || gs[0].UtilPct != 61 || gs[0].MemTotal != 16384*1024*1024 {
		t.Fatalf("%+v", gs[0])
	}
	if gs[0].TempC == nil || *gs[0].TempC != 57 || gs[0].PowerW == nil || *gs[0].PowerW != 175.40 {
		t.Fatalf("temp/power %+v", gs[0])
	}
	if gs[1].TempC != nil || gs[1].PowerW != nil || gs[1].ClockMHz == nil || *gs[1].ClockMHz != 210 {
		t.Fatalf("optional %+v", gs[1])
	}
}

func TestGPUsFromCounters(t *testing.T) {
	engines := []gpuCounter{
		{Instance: "pid_4_luid_0x00000000_0x0000AAAA_phys_0_eng_0_engtype_3D", Value: 40},
		{Instance: "pid_8_luid_0x00000000_0x0000AAAA_phys_0_eng_1_engtype_Copy", Value: 12},
		{Instance: "pid_9_luid_0x00000000_0x0000BBBB_phys_1_eng_0_engtype_3D", Value: 7},
	}
	used := []gpuCounter{
		{Instance: "luid_0x00000000_0x0000AAAA_phys_0", Value: 1024},
		{Instance: "luid_0x00000000_0x0000BBBB_phys_1", Value: 2048},
	}
	limit := []gpuCounter{
		{Instance: "luid_0x00000000_0x0000AAAA_phys_0", Value: 4096},
	}
	gs := gpusFromCounters(engines, used, limit)
	if len(gs) != 2 {
		t.Fatalf("got %d %+v", len(gs), gs)
	}
	if gs[0].UtilPct != 40 || gs[0].MemUsed != 1024 || gs[0].MemTotal != 4096 || gs[0].Name != "GPU 0" {
		t.Fatalf("first %+v", gs[0])
	}
	if gs[1].UtilPct != 7 || gs[1].MemUsed != 2048 || gs[1].Name != "GPU 1" {
		t.Fatalf("second %+v", gs[1])
	}
}

func TestDRMGPUs(t *testing.T) {
	root := t.TempDir()
	card := filepath.Join(root, "card0", "device")
	if err := os.MkdirAll(filepath.Join(card, "hwmon", "hwmon0"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Connector nodes are not GPUs.
	if err := os.MkdirAll(filepath.Join(root, "card0-DP-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(card, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vendor", "0x1002\n")
	write("gpu_busy_percent", "33\n")
	write("mem_info_vram_used", "1048576\n")
	write("mem_info_vram_total", "8589934592\n")
	write("uevent", "DRIVER=amdgpu\nPCI_ID=1002:73BF\n")
	write("hwmon/hwmon0/temp1_input", "47000\n")

	nv := filepath.Join(root, "card1", "device")
	if err := os.MkdirAll(nv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nv, "vendor"), []byte("0x10de\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nv, "gpu_busy_percent"), []byte("10\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gs := drmGPUs(root)
	if len(gs) != 2 {
		t.Fatalf("got %d %+v", len(gs), gs)
	}
	if gs[0].Name != "amdgpu" || gs[0].Vendor != "amd" || gs[0].UtilPct != 33 || gs[0].MemTotal != 8589934592 {
		t.Fatalf("%+v", gs[0])
	}
	if gs[0].TempC == nil || *gs[0].TempC != 47 {
		t.Fatalf("temp %+v", gs[0].TempC)
	}
	if gs[1].Vendor != "nvidia" {
		t.Fatalf("%+v", gs[1])
	}
}

func TestMergeWindowsGPUs(t *testing.T) {
	temp := 46.0
	nv := []entities.GPUInfo{{
		Name: "NVIDIA GeForce RTX 3060", Vendor: "nvidia", UtilPct: 0,
		MemUsed: 700 * 1024 * 1024, MemTotal: 12 * 1024 * 1024 * 1024, TempC: &temp,
	}}
	pdh := []entities.GPUInfo{
		{Name: "GPU 0", UtilPct: 0, MemTotal: 128 * 1024 * 1024},
		{Name: "GPU 1", UtilPct: 28, MemUsed: 700 * 1024 * 1024, MemTotal: 12 * 1024 * 1024 * 1024},
	}
	got := mergeWindowsGPUs(nv, pdh)
	if len(got) != 2 {
		t.Fatalf("got %d %+v", len(got), got)
	}
	if got[0].Name != "NVIDIA GeForce RTX 3060" || got[0].UtilPct != 28 || got[0].TempC == nil || *got[0].TempC != 46 {
		t.Fatalf("nvidia %+v", got[0])
	}
	if got[1].Name != "GPU 0" || got[1].UtilPct != 0 {
		t.Fatalf("igpu %+v", got[1])
	}
	named := assignGPUNames(got, []string{"Intel(R) UHD Graphics", "NVIDIA GeForce RTX 3060"})
	if named[1].Name != "Intel(R) UHD Graphics" || named[1].Vendor != "intel" {
		t.Fatalf("named %+v", named[1])
	}
	if named[0].Name != "NVIDIA GeForce RTX 3060" {
		t.Fatalf("nvidia renamed %+v", named[0])
	}
	kept := dropIdleAdapters(append(named, entities.GPUInfo{Name: "GPU 1"}, entities.GPUInfo{Name: "GPU 2"}))
	if len(kept) != 2 || kept[0].Name != "NVIDIA GeForce RTX 3060" || kept[1].Name != "Intel(R) UHD Graphics" {
		t.Fatalf("%+v", kept)
	}
}

func TestPeakGPUs(t *testing.T) {
	lo, hi, mid := 2.0, 34.0, 9.0
	base := peakGPUs(nil, []entities.GPUInfo{{
		Name: "Apple M1 Pro", UtilPct: 0, RenderPct: &lo, TilerPct: &mid, Cores: 14,
	}})
	got := peakGPUs(base, []entities.GPUInfo{{
		Name: "Apple M1 Pro", UtilPct: 33, RenderPct: &hi, TilerPct: &lo,
	}})
	if len(got) != 1 || got[0].UtilPct != 33 || *got[0].RenderPct != 34 || *got[0].TilerPct != 9 || got[0].Cores != 14 {
		t.Fatalf("%+v", got)
	}
}
