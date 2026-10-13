package entities

import (
	"encoding/json"
	"math"
	"testing"
)

func TestFiniteMakesMetricsEncodable(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	ok := 42.0
	m := Metrics{
		CPUPct:     nan,
		CPUUserPct: &inf,
		CPUWaitPct: &ok,
		Load:       [3]float64{1, inf, math.Inf(-1)},
		Cores:      []CPUCore{{Pct: nan}, {Pct: 7}},
		Temps:      []TempSensor{{Name: "a", C: nan}},
		GPUs:       []GPUInfo{{UtilPct: nan, TempC: &nan}},
	}
	if _, err := json.Marshal(m); err == nil {
		t.Fatal("fixture should not encode before Finite")
	}
	Finite(&m)
	if _, err := json.Marshal(m); err != nil {
		t.Fatalf("still not encodable: %v", err)
	}
	if m.CPUPct != 0 || m.CPUUserPct != nil || m.CPUWaitPct == nil || *m.CPUWaitPct != 42 {
		t.Fatalf("cpu fields: %+v", m)
	}
	if m.Load != [3]float64{1, 0, 0} || m.Cores[0].Pct != 0 || m.Cores[1].Pct != 7 {
		t.Fatalf("load/cores: %v %v", m.Load, m.Cores)
	}
	if m.Temps[0].C != 0 || m.GPUs[0].UtilPct != 0 || m.GPUs[0].TempC != nil {
		t.Fatalf("temps/gpus: %+v %+v", m.Temps, m.GPUs)
	}
	if ok != 42 || !math.IsNaN(nan) {
		t.Fatal("Finite must not write through to the values pointers referred to")
	}
}
