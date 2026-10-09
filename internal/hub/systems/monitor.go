// Package monitor keeps probe-hub's fleet view: it polls online agents for metrics,
// derives rates from cumulative counters and persists history in PostgreSQL.
package monitor

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"govmomi-wmks/internal/esxi"
	"govmomi-wmks/internal/guest"
)

type System struct {
	BiosUUID    string        `json:"biosUuid"`
	Name        string        `json:"name"`
	HostName    string        `json:"hostname"`
	Version     string        `json:"version"`
	RemoteAddr  string        `json:"remoteAddr"`
	Tags        []string      `json:"tags"`
	Online      bool          `json:"online"`
	FirstSeen   time.Time     `json:"firstSeen"`
	LastSeen    time.Time     `json:"lastSeen"`
	ConnectedAt time.Time     `json:"connectedAt"`
	Info        guest.SysInfo `json:"info"`
	Latest      *Sample       `json:"latest,omitempty"`
}

type Sample struct {
	TS           time.Time          `json:"ts"`
	CPUPct       float64            `json:"cpuPct"`
	MemUsed      uint64             `json:"memUsed"`
	MemTotal     uint64             `json:"memTotal"`
	MemPct       float64            `json:"memPct"`
	SwapUsed     uint64             `json:"swapUsed"`
	SwapTotal    uint64             `json:"swapTotal"`
	DiskUsed     uint64             `json:"diskUsed"`
	DiskTotal    uint64             `json:"diskTotal"`
	DiskPct      float64            `json:"diskPct"`
	RootMount    string             `json:"rootMount,omitempty"`
	DiskReadBps  float64            `json:"diskReadBps"`
	DiskWriteBps float64            `json:"diskWriteBps"`
	NetRxBps     float64            `json:"netRxBps"`
	NetTxBps     float64            `json:"netTxBps"`
	Load         [3]float64         `json:"load"`
	TempMax      float64            `json:"tempMax,omitempty"`
	Procs        int                `json:"procs"`
	LatencyMs    int64              `json:"latencyMs"`
	Disks        []guest.DiskUsage  `json:"disks,omitempty"`
	Temps        []guest.TempSensor `json:"temps,omitempty"`
	NICs         []guest.NICInfo    `json:"nics,omitempty"`
}

type Event struct {
	TS     time.Time `json:"ts"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
}

type counters struct {
	at                  time.Time
	read, write, rx, tx uint64
}

type Monitor struct {
	Hub       *guest.Hub
	Store     *Store // nil: live view only, no history
	Interval  time.Duration
	Retention time.Duration

	mu      sync.Mutex
	systems map[string]*System
	prev    map[string]counters
	polling map[string]bool
}

func New(hub *guest.Hub, store *Store, interval, retention time.Duration) *Monitor {
	if interval < 2*time.Second {
		interval = 10 * time.Second
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	m := &Monitor{
		Hub: hub, Store: store, Interval: interval, Retention: retention,
		systems: map[string]*System{},
		prev:    map[string]counters{},
		polling: map[string]bool{},
	}
	hub.OnConnect = m.onConnect
	hub.OnDisconnect = m.onDisconnect
	return m
}

func (m *Monitor) Load(ctx context.Context) error {
	if m.Store == nil {
		return nil
	}
	list, err := m.Store.LoadSystems(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	for _, s := range list {
		s.Online = false
		m.systems[s.BiosUUID] = s
	}
	m.mu.Unlock()
	return nil
}

func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	m.prune(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, s := range m.Hub.List() {
				go m.poll(ctx, s.BiosUUID)
			}
		case <-prune.C:
			m.prune(ctx)
		}
	}
}

func (m *Monitor) prune(ctx context.Context) {
	if m.Store == nil {
		return
	}
	now := time.Now()
	n, err := m.Store.Prune(ctx, now.Add(-m.Retention), now.Add(-30*24*time.Hour))
	if err != nil {
		log.Printf("monitor prune: %v", err)
	} else if n > 0 {
		log.Printf("monitor prune: %d samples older than %s", n, m.Retention)
	}
}

func (m *Monitor) onConnect(info guest.SessionInfo) {
	at := parseTime(info.ConnectedAt)
	m.mu.Lock()
	sys := m.systems[info.BiosUUID]
	if sys == nil {
		sys = &System{BiosUUID: info.BiosUUID, Name: info.HostName, Tags: []string{}, FirstSeen: at}
		m.systems[info.BiosUUID] = sys
	}
	sys.HostName = info.HostName
	sys.Version = info.Version
	sys.RemoteAddr = info.RemoteAddr
	sys.Online = true
	sys.ConnectedAt = at
	sys.LastSeen = at
	snap := *sys
	delete(m.prev, info.BiosUUID)
	m.mu.Unlock()
	if m.Store != nil {
		if err := m.Store.Connect(context.Background(), snap); err != nil {
			log.Printf("monitor connect %s: %v", info.BiosUUID, err)
		}
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		m.poll(context.Background(), info.BiosUUID)
	}()
}

func (m *Monitor) onDisconnect(info guest.SessionInfo) {
	now := time.Now()
	m.mu.Lock()
	if sys := m.systems[info.BiosUUID]; sys != nil {
		sys.Online = false
		sys.LastSeen = now
	}
	delete(m.prev, info.BiosUUID)
	m.mu.Unlock()
	if m.Store != nil {
		if err := m.Store.Disconnect(context.Background(), info.BiosUUID, now); err != nil {
			log.Printf("monitor disconnect %s: %v", info.BiosUUID, err)
		}
	}
}

func (m *Monitor) poll(ctx context.Context, bios string) {
	m.mu.Lock()
	if m.polling[bios] {
		m.mu.Unlock()
		return
	}
	m.polling[bios] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.polling, bios)
		m.mu.Unlock()
	}()

	c, cancel := context.WithTimeout(ctx, guest.ListTimeout)
	defer cancel()
	start := time.Now()
	met, err := m.Hub.Metrics(c, bios)
	if isNotImplemented(err) {
		met, err = m.legacy(c, bios)
	}
	if err != nil {
		log.Printf("monitor poll %s: %v", bios, err)
		return
	}
	smp := m.derive(bios, met, time.Since(start))

	m.mu.Lock()
	sys := m.systems[bios]
	if sys == nil {
		m.mu.Unlock()
		return
	}
	sys.Latest = &smp
	sys.Info = met.Info
	if met.Info.HostName != "" {
		sys.HostName = met.Info.HostName
	}
	sys.LastSeen = smp.TS
	m.mu.Unlock()
	if m.Store != nil {
		if err := m.Store.Record(context.Background(), bios, met.Info, smp); err != nil {
			log.Printf("monitor record %s: %v", bios, err)
		}
	}
}

func (m *Monitor) derive(bios string, met guest.Metrics, latency time.Duration) Sample {
	now := time.Now()
	smp := Sample{
		TS: now.UTC().Truncate(time.Second), CPUPct: met.CPUPct,
		MemUsed: met.MemUsed, MemTotal: met.MemTotal,
		SwapUsed: met.SwapUsed, SwapTotal: met.SwapTotal,
		RootMount: met.RootMount, Load: met.Load, Procs: met.Procs,
		LatencyMs: latency.Milliseconds(),
		Disks:     met.Disks, Temps: met.Temps, NICs: met.NICs,
	}
	if met.MemTotal > 0 {
		smp.MemPct = float64(met.MemUsed) / float64(met.MemTotal) * 100
	}
	for _, d := range met.Disks {
		if d.Mount == met.RootMount {
			smp.DiskUsed, smp.DiskTotal = d.Used, d.Total
		}
	}
	if smp.DiskTotal == 0 {
		for _, d := range met.Disks {
			smp.DiskUsed += d.Used
			smp.DiskTotal += d.Total
		}
	}
	if smp.DiskTotal > 0 {
		smp.DiskPct = float64(smp.DiskUsed) / float64(smp.DiskTotal) * 100
	}
	for _, t := range met.Temps {
		if t.C > smp.TempMax {
			smp.TempMax = t.C
		}
	}
	cur := counters{at: now, read: met.DiskRead, write: met.DiskWrite, rx: met.NetRx, tx: met.NetTx}
	m.mu.Lock()
	prev, ok := m.prev[bios]
	m.prev[bios] = cur
	m.mu.Unlock()
	if ok {
		dt := cur.at.Sub(prev.at).Seconds()
		if dt > 0.5 {
			smp.DiskReadBps = rate(prev.read, cur.read, dt)
			smp.DiskWriteBps = rate(prev.write, cur.write, dt)
			smp.NetRxBps = rate(prev.rx, cur.rx, dt)
			smp.NetTxBps = rate(prev.tx, cur.tx, dt)
		}
	}
	return smp
}

// legacy maps a pre-1.2 agent's health reply onto Metrics so old agents still show up.
func (m *Monitor) legacy(ctx context.Context, bios string) (guest.Metrics, error) {
	h, err := m.Hub.Health(ctx, bios)
	if err != nil {
		return guest.Metrics{}, err
	}
	out := guest.Metrics{
		Info:     guest.SysInfo{CPUModel: h.CPU.Model, Threads: h.CPU.LogicalCPUs},
		CPUPct:   h.CPU.Pct,
		MemTotal: uint64(h.Memory.TotalMB) << 20,
		MemUsed:  uint64(h.Memory.TotalMB-h.Memory.AvailMB) << 20,
		NICs:     h.NICs,
	}
	for _, d := range h.Disks {
		out.Disks = append(out.Disks, guest.DiskUsage{Mount: d.Name, Total: uint64(d.TotalGB * 1e9), Used: uint64(d.UsedGB * 1e9)})
	}
	if len(out.Disks) > 0 {
		out.RootMount = out.Disks[0].Mount
	}
	return out, nil
}

func rate(a, b uint64, dt float64) float64 {
	if b < a {
		return 0
	}
	return float64(b-a) / dt
}

func isNotImplemented(err error) bool {
	var api *esxi.Error
	return errors.As(err, &api) && api.Code == "not_implemented"
}

func (m *Monitor) List() []System {
	m.mu.Lock()
	out := make([]System, 0, len(m.systems))
	for _, s := range m.systems {
		out = append(out, *s)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a == b {
			return out[i].BiosUUID < out[j].BiosUUID
		}
		return a < b
	})
	return out
}

func (m *Monitor) Get(bios string) (System, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.systems[strings.ToLower(bios)]
	if s == nil {
		return System{}, false
	}
	return *s, true
}

func (m *Monitor) Update(ctx context.Context, bios string, name *string, tags []string) (System, error) {
	bios = strings.ToLower(bios)
	if tags != nil {
		tags = cleanTags(tags)
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		name = &n
	}
	if m.Store != nil {
		if err := m.Store.UpdateMeta(ctx, bios, name, tags); err != nil && !errors.Is(err, errNotFound) {
			return System{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.systems[bios]
	if s == nil {
		return System{}, errNotFound
	}
	if name != nil {
		s.Name = *name
		if s.Name == "" {
			s.Name = s.HostName
		}
	}
	if tags != nil {
		s.Tags = tags
	}
	return *s, nil
}

func (m *Monitor) Delete(ctx context.Context, bios string) error {
	bios = strings.ToLower(bios)
	if m.Hub.Online(bios) {
		return esxi.ErrBad("system is online; stop the agent first")
	}
	if m.Store != nil {
		if err := m.Store.Delete(ctx, bios); err != nil {
			return err
		}
	}
	m.mu.Lock()
	delete(m.systems, bios)
	m.mu.Unlock()
	return nil
}

func cleanTags(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now()
	}
	return t
}
