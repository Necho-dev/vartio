package systems

import (
	"encoding/json"
	"testing"

	"vartio/internal/entities"
)

func TestGPUUtilsJSON(t *testing.T) {
	if gpuUtilsJSON(nil) != nil {
		t.Fatal("empty should be nil")
	}
	raw := gpuUtilsJSON([]entities.GPUInfo{
		{Name: "Apple M1 Pro", UtilPct: 23, RenderPct: f64(21), TilerPct: f64(3)},
		{Name: "Apple M1 Pro", UtilPct: 4},
		{Name: "", UtilPct: 1},
	})
	var m map[string]float64
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["Apple M1 Pro"] != 23 || m["Apple M1 Pro 渲染"] != 21 || m["Apple M1 Pro Tiler"] != 3 || m["Apple M1 Pro 2"] != 4 || m["GPU"] != 1 {
		t.Fatalf("%v", m)
	}
}

func f64(v float64) *float64 { return &v }

func TestTempSensorsJSON(t *testing.T) {
	if tempSensorsJSON(nil) != nil {
		t.Fatal("empty should be nil")
	}
	raw := tempSensorsJSON([]entities.TempSensor{{Name: "PMU tdie0", C: 52.5}, {Name: "NAND CH0 temp", C: 42}})
	var m map[string]float64
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["PMU tdie0"] != 52.5 || m["NAND CH0 temp"] != 42 {
		t.Fatalf("%v", m)
	}
}
