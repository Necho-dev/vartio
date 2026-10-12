package systems

import (
	"encoding/json"
	"fmt"

	"vartio/internal/entities"
)

// gpuUtilsJSON maps each GPU name to its utilization for the history column.
// Apple also stores renderer and tiler under "name 渲染" and "name Tiler".
// A repeated name gets a numeric suffix so two devices do not overwrite each other.
func gpuUtilsJSON(gs []entities.GPUInfo) []byte {
	if len(gs) == 0 {
		return nil
	}
	seen := map[string]int{}
	out := make(map[string]float64, len(gs))
	for _, g := range gs {
		name := g.Name
		if name == "" {
			name = "GPU"
		}
		seen[name]++
		if seen[name] > 1 {
			name = fmt.Sprintf("%s %d", name, seen[name])
		}
		out[name] = g.UtilPct
		if g.RenderPct != nil {
			out[name+" 渲染"] = *g.RenderPct
		}
		if g.TilerPct != nil {
			out[name+" Tiler"] = *g.TilerPct
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// tempSensorsJSON maps each sensor name to Celsius for the history column.
func tempSensorsJSON(ts []entities.TempSensor) []byte {
	if len(ts) == 0 {
		return nil
	}
	seen := map[string]int{}
	out := make(map[string]float64, len(ts))
	for _, t := range ts {
		name := t.Name
		if name == "" {
			name = "sensor"
		}
		seen[name]++
		if seen[name] > 1 {
			name = fmt.Sprintf("%s %d", name, seen[name])
		}
		out[name] = t.C
	}
	b, _ := json.Marshal(out)
	return b
}
