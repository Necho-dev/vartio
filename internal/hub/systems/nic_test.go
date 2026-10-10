package monitor

import "testing"

func TestNicRates(t *testing.T) {
	prev := map[string][2]uint64{"eth0": {1000, 200}, "eth1": {0, 0}}
	cur := map[string][2]uint64{"eth0": {11000, 5200}, "eth1": {500, 0}}
	got := nicRates(prev, cur, 10)
	if len(got) != 2 {
		t.Fatalf("n=%d", len(got))
	}
	if got[0].Name != "eth0" || got[0].RxBps != 1000 || got[0].TxBps != 500 || got[0].RxBytes != 10000 {
		t.Fatalf("eth0 %+v", got[0])
	}
	if nicRates(nil, cur, 10) != nil {
		t.Fatal("no previous")
	}
	if nicRates(prev, cur, 0) != nil {
		t.Fatal("no dt")
	}
}
