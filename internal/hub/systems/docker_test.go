package systems

import (
	"strings"
	"testing"
	"vartio/internal/entities"
)

func TestDockerSampleRates(t *testing.T) {
	prev := map[string]entities.ContainerStat{"a": {ID: "a", Stats: true, CPUNs: 1e9, SysNs: 100e9, CPUs: 4, NetRx: 1000, NetTx: 0, BlkWrite: 500}}
	d := &entities.DockerInfo{Engine: "Docker Engine", Version: "29", Running: 2, Total: 3, Containers: []entities.ContainerStat{
		{ID: "a", Name: "web", State: "running", Stats: true, CPUNs: 3e9, SysNs: 110e9, CPUs: 4, NetRx: 11000, NetTx: 5000, BlkWrite: 1500, MemUsed: 64 << 20},
		{ID: "b", Name: "new", State: "running", Stats: true, CPUNs: 5e9, SysNs: 110e9, CPUs: 4},
		{ID: "c", Name: "off", State: "exited"},
	}}
	out := dockerSample(d, prev, 10)
	if out.Engine != "Docker Engine" || out.Running != 2 || out.Total != 3 || len(out.Containers) != 3 {
		t.Fatalf("summary %+v", out)
	}
	web := out.Containers[0]
	// 2s of CPU over 10s of 4-core host time = 20% of the host = 80% of one core.
	if web.CPUPct == nil || *web.CPUPct != 80 || *web.NetRxBps != 1000 || *web.NetTxBps != 500 || *web.BlkWriteBps != 100 || web.MemUsed != 64<<20 {
		t.Fatalf("web %+v cpu=%v", web, web.CPUPct)
	}
	if out.Containers[1].CPUPct != nil || out.Containers[2].CPUPct != nil {
		t.Fatal("no rates without a previous sample")
	}
	// Engines without host CPU time fall back to wall time.
	prev["a"] = entities.ContainerStat{ID: "a", Stats: true, CPUNs: 1e9}
	d.Containers[0].SysNs = 0
	if got := *dockerSample(d, prev, 4).Containers[0].CPUPct; got != 50 {
		t.Fatalf("wall-time cpu = %v", got)
	}
	// Engine read timestamps beat the hub's polling interval.
	prev["a"] = entities.ContainerStat{ID: "a", Stats: true, CPUNs: 1e9, ReadNs: 10e9}
	d.Containers[0].ReadNs = 12e9
	if got := *dockerSample(d, prev, 4).Containers[0].CPUPct; got != 100 {
		t.Fatalf("read-time cpu = %v", got)
	}
	if dockerSample(d, prev, 0).Containers[0].CPUPct != nil || dockerSample(nil, prev, 10) != nil {
		t.Fatal("dt 0 / nil docker")
	}
	// Counter glitches are dropped rather than plotted.
	prev["a"] = entities.ContainerStat{ID: "a", Stats: true, CPUNs: 1e9, CPUs: 4}
	d.Containers[0] = entities.ContainerStat{ID: "a", Stats: true, CPUNs: 100e9, CPUs: 4, Ports: "8080→80/tcp", Update: true}
	if c := dockerSample(d, prev, 10).Containers[0]; c.CPUPct != nil || c.Ports != "8080→80/tcp" || !c.Update {
		t.Fatalf("glitch %+v", c)
	}
	d.Containers[0] = entities.ContainerStat{ID: "a", Stats: true, CPUNs: 1e9, CPUs: 4, NetRx: 1 << 40}
	if dockerSample(d, prev, 10).Containers[0].NetRxBps != nil {
		t.Fatal("impossible network rate kept")
	}
}

func TestContainerEvents(t *testing.T) {
	prev := &DockerSample{Containers: []ContainerSample{
		{Name: "web", State: "running", Health: "healthy"},
		{Name: "db", State: "running"},
		{Name: "job", State: "exited"},
		{Name: "gone", State: "running"},
	}}
	cur := &DockerSample{Containers: []ContainerSample{
		{Name: "web", State: "running", Health: "unhealthy"},
		{Name: "db", State: "exited", Status: "Exited (137) 1 second ago"},
		{Name: "job", State: "running"},
		{Name: "fresh", State: "running"},
	}}
	got := map[string]Event{}
	for _, e := range containerEvents(prev, cur) {
		got[e.Kind] = e
	}
	if e := got["container.stop"]; e.Level != LevelWarn || !strings.Contains(e.Detail, "db（Exited (137)") || !strings.Contains(e.Detail, "gone（已删除）") {
		t.Fatalf("stop %+v", e)
	}
	if e := got["container.start"]; e.Detail != "fresh、job" {
		t.Fatalf("start %+v", e)
	}
	if e := got["container.unhealthy"]; e.Detail != "web" || e.Level != LevelWarn {
		t.Fatalf("unhealthy %+v", e)
	}
	if len(containerEvents(prev, &DockerSample{Error: "engine down"})) != 0 || len(containerEvents(nil, cur)) != 0 {
		t.Fatal("engine errors and first samples are quiet")
	}
	back := containerEvents(cur, &DockerSample{Containers: []ContainerSample{{Name: "web", State: "running", Health: "healthy"}, {Name: "db", State: "exited"}, {Name: "job", State: "running"}, {Name: "fresh", State: "running"}}})
	if len(back) != 1 || back[0].Kind != "container.healthy" {
		t.Fatalf("recovery %+v", back)
	}
}
