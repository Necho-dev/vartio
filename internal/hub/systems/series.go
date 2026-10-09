package monitor

import "time"

// Series is columnar so a week of buckets stays a small JSON payload.
type Series struct {
	StepSec   int        `json:"stepSec"`
	T         []int64    `json:"t"`
	CPU       []float64  `json:"cpu"`
	CPUMax    []float64  `json:"cpuMax"`
	Mem       []float64  `json:"mem"`
	MemTotal  []float64  `json:"memTotal"`
	Swap      []float64  `json:"swap"`
	Disk      []float64  `json:"disk"`
	DiskTotal []float64  `json:"diskTotal"`
	DiskRead  []float64  `json:"diskRead"`
	DiskWrite []float64  `json:"diskWrite"`
	NetRx     []float64  `json:"netRx"`
	NetTx     []float64  `json:"netTx"`
	Load1     []float64  `json:"load1"`
	Temp      []*float64 `json:"temp"`
	Procs     []float64  `json:"procs"`
}

func newSeries(step time.Duration) *Series {
	return &Series{
		StepSec: int(step / time.Second),
		T:       []int64{}, CPU: []float64{}, CPUMax: []float64{}, Mem: []float64{}, MemTotal: []float64{},
		Swap: []float64{}, Disk: []float64{}, DiskTotal: []float64{}, DiskRead: []float64{}, DiskWrite: []float64{},
		NetRx: []float64{}, NetTx: []float64{}, Load1: []float64{}, Temp: []*float64{}, Procs: []float64{},
	}
}

func (s *Series) add(t time.Time, cpu, cpuMax, mem, memT, swap, disk, diskT, dr, dw, rx, tx, l1 float64, temp *float64, procs float64) {
	s.T = append(s.T, t.UnixMilli())
	s.CPU = append(s.CPU, cpu)
	s.CPUMax = append(s.CPUMax, cpuMax)
	s.Mem = append(s.Mem, mem)
	s.MemTotal = append(s.MemTotal, memT)
	s.Swap = append(s.Swap, swap)
	s.Disk = append(s.Disk, disk)
	s.DiskTotal = append(s.DiskTotal, diskT)
	s.DiskRead = append(s.DiskRead, dr)
	s.DiskWrite = append(s.DiskWrite, dw)
	s.NetRx = append(s.NetRx, rx)
	s.NetTx = append(s.NetTx, tx)
	s.Load1 = append(s.Load1, l1)
	s.Temp = append(s.Temp, temp)
	s.Procs = append(s.Procs, procs)
}

var ranges = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

func stepFor(span, interval time.Duration) time.Duration {
	step := span / 240
	if step < interval {
		step = interval
	}
	return step.Round(time.Second)
}
