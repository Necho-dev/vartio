package systems

import (
	"time"
	"vartio/internal/entities"
)

// Series is columnar so a week of buckets stays a small JSON payload.
type Series struct {
	StepSec   int       `json:"stepSec"`
	T         []int64   `json:"t"`
	CPU       []float64 `json:"cpu"`
	CPUMax    []float64 `json:"cpuMax"`
	Mem       []float64 `json:"mem"`
	MemTotal  []float64 `json:"memTotal"`
	Swap      []float64 `json:"swap"`
	Disk      []float64 `json:"disk"`
	DiskTotal []float64 `json:"diskTotal"`
	DiskRead  []float64 `json:"diskRead"`
	DiskWrite []float64 `json:"diskWrite"`
	NetRx     []float64 `json:"netRx"`
	NetTx     []float64 `json:"netTx"`
	NetRxMax  []float64 `json:"netRxMax"`
	NetTxMax  []float64 `json:"netTxMax"`
	// NetRxBytes/NetTxBytes are bytes moved within each bucket.
	NetRxBytes []float64 `json:"netRxBytes"`
	NetTxBytes []float64 `json:"netTxBytes"`
	Load1      []float64 `json:"load1"`
	// Nullable columns are nil for buckets recorded before they were collected.
	Load5    []*float64 `json:"load5"`
	Load15   []*float64 `json:"load15"`
	Temp     []*float64 `json:"temp"`
	Procs    []float64  `json:"procs"`
	NetRxPps []*float64 `json:"netRxPps"`
	NetTxPps []*float64 `json:"netTxPps"`
	// NetErrs/NetDrops are counts within each bucket.
	NetErrs     []*float64 `json:"netErrs"`
	NetDrops    []*float64 `json:"netDrops"`
	TCPEstab    []*float64 `json:"tcpEstab"`
	TCPTimeWait []*float64 `json:"tcpTimeWait"`
	TCPListen   []*float64 `json:"tcpListen"`
	Retrans     []*float64 `json:"retrans"`
	Latency     []*float64 `json:"latency"`
	// TCPOutSegs/TCPRetransSegs are sums within each bucket (bytes on macOS).
	TCPOutSegs     []*float64 `json:"tcpOutSegs"`
	TCPRetransSegs []*float64 `json:"tcpRetransSegs"`
	// Socket counts by protocol (agent >= 1.15.0).
	ConnTCP   []*float64 `json:"connTcp"`
	ConnUDP   []*float64 `json:"connUdp"`
	ConnOther []*float64 `json:"connOther"`
	// DiskParts maps mount point -> used bytes per bucket (nil = no sample).
	DiskParts map[string][]*float64 `json:"diskParts"`
	// NetNics is per-interface throughput; missing until the agent reports NIC counters.
	NetNics map[string]NicLine `json:"netNics,omitempty"`
}

// NicLine is one NIC's series, aligned to Series.T.
type NicLine struct {
	Rx      []*float64 `json:"rx"`
	Tx      []*float64 `json:"tx"`
	RxBytes []*float64 `json:"rxBytes"`
	TxBytes []*float64 `json:"txBytes"`
}

func newSeries(step time.Duration) *Series {
	return &Series{
		StepSec: int(step / time.Second),
		T:       []int64{}, CPU: []float64{}, CPUMax: []float64{}, Mem: []float64{}, MemTotal: []float64{},
		Swap: []float64{}, Disk: []float64{}, DiskTotal: []float64{}, DiskRead: []float64{}, DiskWrite: []float64{},
		NetRx: []float64{}, NetTx: []float64{}, NetRxMax: []float64{}, NetTxMax: []float64{}, NetRxBytes: []float64{}, NetTxBytes: []float64{},
		Load1: []float64{}, Load5: []*float64{}, Load15: []*float64{}, Temp: []*float64{}, Procs: []float64{},
		NetRxPps: []*float64{}, NetTxPps: []*float64{}, NetErrs: []*float64{}, NetDrops: []*float64{},
		TCPEstab: []*float64{}, TCPTimeWait: []*float64{}, TCPListen: []*float64{}, Retrans: []*float64{}, Latency: []*float64{},
		TCPOutSegs: []*float64{}, TCPRetransSegs: []*float64{},
		ConnTCP: []*float64{}, ConnUDP: []*float64{}, ConnOther: []*float64{},
		DiskParts: map[string][]*float64{},
		NetNics:   map[string]NicLine{},
	}
}

// bucket is one row of the Series query.
type bucket struct {
	cpu, cpuMax, mem, memT, swap, disk, diskT, dr, dw float64
	rx, tx, rxMax, txMax, rxBytes, txBytes, l1, procs float64
	l5, l15, temp, rxPps, txPps, errs, drops          *float64
	estab, tw, listen, retrans, latency               *float64
	outSegs, retransSegs, connTCP, connUDP, connOther *float64
}

func (s *Series) add(t time.Time, b bucket) {
	s.T = append(s.T, t.UnixMilli())
	s.CPU = append(s.CPU, b.cpu)
	s.CPUMax = append(s.CPUMax, b.cpuMax)
	s.Mem = append(s.Mem, b.mem)
	s.MemTotal = append(s.MemTotal, b.memT)
	s.Swap = append(s.Swap, b.swap)
	s.Disk = append(s.Disk, b.disk)
	s.DiskTotal = append(s.DiskTotal, b.diskT)
	s.DiskRead = append(s.DiskRead, b.dr)
	s.DiskWrite = append(s.DiskWrite, b.dw)
	s.NetRx = append(s.NetRx, b.rx)
	s.NetTx = append(s.NetTx, b.tx)
	s.NetRxMax = append(s.NetRxMax, b.rxMax)
	s.NetTxMax = append(s.NetTxMax, b.txMax)
	s.NetRxBytes = append(s.NetRxBytes, b.rxBytes)
	s.NetTxBytes = append(s.NetTxBytes, b.txBytes)
	s.Load1 = append(s.Load1, b.l1)
	s.Load5 = append(s.Load5, b.l5)
	s.Load15 = append(s.Load15, b.l15)
	s.Temp = append(s.Temp, b.temp)
	s.Procs = append(s.Procs, b.procs)
	s.NetRxPps = append(s.NetRxPps, b.rxPps)
	s.NetTxPps = append(s.NetTxPps, b.txPps)
	s.NetErrs = append(s.NetErrs, b.errs)
	s.NetDrops = append(s.NetDrops, b.drops)
	s.TCPEstab = append(s.TCPEstab, b.estab)
	s.TCPTimeWait = append(s.TCPTimeWait, b.tw)
	s.TCPListen = append(s.TCPListen, b.listen)
	s.Retrans = append(s.Retrans, b.retrans)
	s.Latency = append(s.Latency, b.latency)
	s.TCPOutSegs = append(s.TCPOutSegs, b.outSegs)
	s.TCPRetransSegs = append(s.TCPRetransSegs, b.retransSegs)
	s.ConnTCP = append(s.ConnTCP, b.connTCP)
	s.ConnUDP = append(s.ConnUDP, b.connUDP)
	s.ConnOther = append(s.ConnOther, b.connOther)
}

var ranges = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

func stepFor(span, interval time.Duration) time.Duration {
	step := max(span/240, interval)
	return step.Round(time.Second)
}

func dedupePools(disks []entities.DiskUsage, root string) []entities.DiskUsage {
	return entities.DedupePools(disks, root)
}
