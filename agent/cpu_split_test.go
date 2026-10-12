package agent

import (
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestCPUParts(t *testing.T) {
	prev := cpu.TimesStat{User: 10, Nice: 1, System: 4, Idle: 80, Iowait: 2, Irq: 1, Steal: 2}
	cur := cpu.TimesStat{User: 20, Nice: 1, System: 8, Idle: 164, Iowait: 4, Irq: 1, Steal: 2}
	total, user, system, wait, ok := cpuParts(prev, cur)
	if !ok {
		t.Fatal("expected a delta")
	}
	// 100 ticks: user 10, system 4, wait 2, idle 80, plus the irq/steal that did not move.
	if user < 9.9 || user > 10.1 || system < 3.9 || system > 4.1 || wait < 1.9 || wait > 2.1 || total < 15.9 || total > 16.1 {
		t.Fatalf("user %.2f system %.2f wait %.2f total %.2f", user, system, wait, total)
	}
	if _, _, _, _, ok := cpuParts(cur, prev); ok {
		t.Fatal("backwards counters are not a sample")
	}
}

func TestPerfKinds(t *testing.T) {
	got := perfKinds(8, 6, 2)
	if len(got) != 8 || got[0] != "p" || got[5] != "p" || got[6] != "e" || got[7] != "e" {
		t.Fatalf("%v", got)
	}
	if perfKinds(8, 6, 4) != nil || perfKinds(20, 0, 0) != nil {
		t.Fatal("mismatched counts should stay unlabeled")
	}
}

func TestCapacityKinds(t *testing.T) {
	got := capacityKinds([]int{1024, 1024, 400, 400})
	if len(got) != 4 || got[0] != "p" || got[2] != "e" {
		t.Fatalf("%v", got)
	}
	if capacityKinds([]int{1024, 1024}) != nil {
		t.Fatal("equal cores are unlabeled")
	}
}
