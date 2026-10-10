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

// System is one monitored machine, keyed by the agent's device fingerprint (ID).
// Agents before 1.8 have no fingerprint and use their BIOS UUID as ID.
type System struct {
	ID          string             `json:"id"`
	BiosUUID    string             `json:"biosUuid"`
	Device      *guest.DeviceFacts `json:"device,omitempty"`
	Name        string             `json:"name"`
	HostName    string             `json:"hostname"`
	Version     string             `json:"version"`
	RemoteAddr  string             `json:"remoteAddr"`
	Tags        []string           `json:"tags"`
	Online      bool               `json:"online"`
	FirstSeen   time.Time          `json:"firstSeen"`
	LastSeen    time.Time          `json:"lastSeen"`
	ConnectedAt time.Time          `json:"connectedAt"`
	Info        guest.SysInfo      `json:"info"`
	Latest      *Sample            `json:"latest,omitempty"`
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

// Event categories and levels.
const (
	CatLifecycle = "lifecycle"
	CatChange    = "change"
	CatAudit     = "audit"

	LevelInfo = "info"
	LevelWarn = "warn"
)

// Event is one entry in a system's history. IP and Version describe the agent
// (its source address and build) at the time; Actor is the operator's IP for audits.
type Event struct {
	TS       time.Time `json:"ts"`
	Kind     string    `json:"kind"`
	Category string    `json:"category"`
	Level    string    `json:"level"`
	IP       string    `json:"ip,omitempty"`
	Version  string    `json:"version,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	Detail   string    `json:"detail,omitempty"`
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

	mu       sync.Mutex
	systems  map[string]*System
	prev     map[string]counters
	polling  map[string]bool
	rejected map[string]time.Time
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
		systems:  map[string]*System{},
		prev:     map[string]counters{},
		polling:  map[string]bool{},
		rejected: map[string]time.Time{},
	}
	hub.OnConnect = m.onConnect
	hub.OnDisconnect = m.onDisconnect
	hub.OnReject = m.onReject
	hub.OnAudit = m.onAudit
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
		m.systems[s.ID] = s
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
				go m.poll(ctx, s.DeviceID)
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
	key := info.DeviceID
	var events []Event
	m.mu.Lock()
	var old *System
	why, oldOnline := "", false
	if m.systems[key] == nil {
		old, why = m.adoptLocked(info)
		oldOnline = old != nil && old.Online
	}
	m.mu.Unlock()
	// The old agent may still look online for a moment after a restart; only a
	// session that no longer answers pings is the same machine coming back.
	if oldOnline {
		if m.Hub.Alive(old.ID) {
			old = nil
		} else {
			m.Hub.Kick(old.ID)
		}
	}
	m.mu.Lock()
	sys := m.systems[key]
	rekeyFrom := ""
	if sys == nil && old != nil && m.systems[old.ID] == old {
		rekeyFrom = old.ID
		delete(m.systems, old.ID)
		delete(m.prev, old.ID)
		old.ID = key
		sys = old
		m.systems[key] = sys
		events = append(events, Event{Kind: "rekey", Category: CatLifecycle, Level: LevelWarn,
			Detail: why + "：" + rekeyFrom + " → " + key})
	}
	if sys == nil {
		sys = &System{ID: key, Name: info.HostName, Tags: []string{}, FirstSeen: at}
		m.systems[key] = sys
	}
	events = append(events, Event{Kind: "online", Category: CatLifecycle, Level: LevelInfo})
	if sys.Version != "" && info.Version != "" && sys.Version != info.Version {
		events = append(events, Event{Kind: "upgrade", Category: CatLifecycle, Level: LevelInfo, Detail: sys.Version + " → " + info.Version})
	}
	if sys.RemoteAddr != "" && info.RemoteAddr != "" && sys.RemoteAddr != info.RemoteAddr {
		events = append(events, Event{Kind: "remote", Category: CatChange, Level: LevelInfo, Detail: sys.RemoteAddr + " → " + info.RemoteAddr})
	}
	sys.BiosUUID = info.BiosUUID
	if info.Device != nil {
		sys.Device = info.Device
	}
	sys.HostName = info.HostName
	sys.Version = info.Version
	sys.RemoteAddr = info.RemoteAddr
	sys.Online = true
	sys.ConnectedAt = at
	sys.LastSeen = at
	snap := *sys
	delete(m.prev, key)
	m.mu.Unlock()
	if m.Store != nil {
		ctx := context.Background()
		if rekeyFrom != "" {
			if err := m.Store.Rekey(ctx, rekeyFrom, key); err != nil {
				log.Printf("monitor rekey %s -> %s: %v", rekeyFrom, key, err)
			}
		}
		if err := m.Store.Connect(ctx, snap); err != nil {
			log.Printf("monitor connect %s: %v", key, err)
		}
	}
	m.emit(key, time.Now(), events...)
	go func() {
		time.Sleep(300 * time.Millisecond)
		m.poll(context.Background(), key)
	}()
}

// adoptLocked finds the record a newly fingerprinted agent should inherit: its own
// pre-fingerprint row (keyed by BIOS UUID), or a row whose fingerprint inputs still
// match on two of three (e.g. only the NIC changed). Ambiguity adopts nothing. An
// online candidate is returned too; the caller checks it is really gone.
func (m *Monitor) adoptLocked(info guest.SessionInfo) (*System, string) {
	var hit *System
	why := ""
	n := 0
	for _, s := range m.systems {
		switch {
		case s.Device == nil && s.ID == info.BiosUUID && info.DeviceID != info.BiosUUID:
			hit, why = s, "BIOS UUID 记录迁移为设备指纹"
		case s.Device != nil && info.Device != nil && s.Device.Matches(info.Device) >= 2:
			hit, why = s, "设备指纹变更（"+changedPart(s.Device, info.Device)+"）"
		default:
			continue
		}
		n++
	}
	if n != 1 {
		return nil, ""
	}
	return hit, why
}

func changedPart(a, b *guest.DeviceFacts) string {
	switch {
	case a.MAC != b.MAC:
		return "主网卡 MAC 变化"
	case a.HW != b.HW:
		return "硬件 UUID 变化"
	default:
		return "系统安装 ID 变化"
	}
}

func (m *Monitor) onDisconnect(info guest.SessionInfo) {
	now := time.Now()
	key := info.DeviceID
	detail := ""
	m.mu.Lock()
	sys := m.systems[key]
	if sys == nil || !sys.Online {
		// already re-keyed to a new fingerprint, or never registered
		m.mu.Unlock()
		return
	}
	sys.Online = false
	sys.LastSeen = now
	if !sys.ConnectedAt.IsZero() {
		detail = "在线 " + now.Sub(sys.ConnectedAt).Round(time.Second).String()
	}
	delete(m.prev, key)
	m.mu.Unlock()
	if m.Store != nil {
		if err := m.Store.Disconnect(context.Background(), key, now); err != nil {
			log.Printf("monitor disconnect %s: %v", key, err)
		}
	}
	m.emit(key, now, Event{Kind: "offline", Category: CatLifecycle, Level: LevelInfo, Detail: detail})
}

// onReject logs a fingerprint collision, at most every 10 minutes per device, since
// the refused agent keeps retrying.
func (m *Monitor) onReject(hello guest.Hello, remote, _ string) {
	key := hello.Key()
	now := time.Now()
	m.mu.Lock()
	_, known := m.systems[key]
	last := m.rejected[key]
	if known && now.Sub(last) >= 10*time.Minute {
		m.rejected[key] = now
	} else {
		known = false
	}
	m.mu.Unlock()
	if known {
		m.emit(key, now, Event{Kind: "conflict", Category: CatLifecycle, Level: LevelWarn, IP: remote, Version: hello.Version,
			Detail: "同指纹设备重复接入已拒绝（对方主机名 " + hello.HostName + "，BIOS " + hello.BiosUUID + "）"})
	}
}

func (m *Monitor) onAudit(a guest.Audit) {
	e := Event{Kind: a.Kind, Category: CatAudit, Level: LevelInfo, Actor: a.Actor, Detail: a.Detail}
	if a.Err != "" {
		e.Level = LevelWarn
		e.Detail = strings.TrimSpace(e.Detail + " · 失败：" + a.Err)
	}
	m.emit(a.Key, time.Now(), e)
}

// emit stores events, stamping the agent's current IP and version where unset.
func (m *Monitor) emit(id string, at time.Time, events ...Event) {
	if m.Store == nil || len(events) == 0 {
		return
	}
	m.mu.Lock()
	var ip, ver string
	if s := m.systems[id]; s != nil {
		ip, ver = s.RemoteAddr, s.Version
	}
	m.mu.Unlock()
	for _, e := range events {
		if e.TS.IsZero() {
			e.TS = at
		}
		if e.IP == "" {
			e.IP = ip
		}
		if e.Version == "" {
			e.Version = ver
		}
		if err := m.Store.AddEvent(context.Background(), id, e); err != nil {
			log.Printf("monitor event %s %s: %v", id, e.Kind, err)
		}
	}
}

func (m *Monitor) poll(ctx context.Context, id string) {
	m.mu.Lock()
	if m.polling[id] {
		m.mu.Unlock()
		return
	}
	m.polling[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.polling, id)
		m.mu.Unlock()
	}()

	c, cancel := context.WithTimeout(ctx, guest.ListTimeout)
	defer cancel()
	start := time.Now()
	met, err := m.Hub.Metrics(c, id)
	if isNotImplemented(err) {
		met, err = m.legacy(c, id)
	}
	if err != nil {
		log.Printf("monitor poll %s: %v", id, err)
		return
	}
	smp := m.derive(id, met, time.Since(start))

	m.mu.Lock()
	sys := m.systems[id]
	if sys == nil {
		m.mu.Unlock()
		return
	}
	events := diff(sys.Info, sys.Latest, met.Info, &smp)
	sys.Latest = &smp
	sys.Info = met.Info
	if met.Info.HostName != "" {
		sys.HostName = met.Info.HostName
	}
	sys.LastSeen = smp.TS
	m.mu.Unlock()
	if m.Store != nil {
		if err := m.Store.Record(context.Background(), id, met.Info, smp); err != nil {
			log.Printf("monitor record %s: %v", id, err)
		}
	}
	m.emit(id, smp.TS, events...)
}

func (m *Monitor) derive(id string, met guest.Metrics, latency time.Duration) Sample {
	now := time.Now()
	smp := Sample{
		TS: now.UTC().Truncate(time.Second), CPUPct: met.CPUPct,
		MemUsed: met.MemUsed, MemTotal: met.MemTotal,
		SwapUsed: met.SwapUsed, SwapTotal: met.SwapTotal,
		RootMount: met.RootMount, Load: met.Load, Procs: met.Procs,
		LatencyMs: latency.Milliseconds(),
		Disks:     dedupePools(met.Disks, met.RootMount), Temps: met.Temps, NICs: met.NICs,
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
	prev, ok := m.prev[id]
	m.prev[id] = cur
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
func (m *Monitor) legacy(ctx context.Context, id string) (guest.Metrics, error) {
	h, err := m.Hub.Health(ctx, id)
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
			return out[i].ID < out[j].ID
		}
		return a < b
	})
	return out
}

// Get finds a system by device id, or by BIOS UUID when exactly one system has it.
func (m *Monitor) Get(id string) (System, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.findLocked(id)
	if s == nil {
		return System{}, false
	}
	return *s, true
}

func (m *Monitor) findLocked(id string) *System {
	id = strings.ToLower(strings.TrimSpace(id))
	if s := m.systems[id]; s != nil {
		return s
	}
	var hit *System
	for _, s := range m.systems {
		if s.BiosUUID == id {
			if hit != nil {
				return nil
			}
			hit = s
		}
	}
	return hit
}

func (m *Monitor) Update(ctx context.Context, id, actor string, name *string, tags []string) (System, error) {
	if tags != nil {
		tags = cleanTags(tags)
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		name = &n
	}
	m.mu.Lock()
	s := m.findLocked(id)
	if s == nil {
		m.mu.Unlock()
		return System{}, errNotFound
	}
	var changes []string
	if name != nil && *name != s.Name && !(*name == "" && s.Name == s.HostName) {
		changes = append(changes, "名称 "+s.Name+" → "+*name)
	}
	if tags != nil && strings.Join(tags, ",") != strings.Join(s.Tags, ",") {
		changes = append(changes, "标签 ["+strings.Join(s.Tags, ", ")+"] → ["+strings.Join(tags, ", ")+"]")
	}
	key := s.ID
	m.mu.Unlock()
	if m.Store != nil {
		if err := m.Store.UpdateMeta(ctx, key, name, tags); err != nil && !errors.Is(err, errNotFound) {
			return System{}, err
		}
	}
	m.mu.Lock()
	s = m.systems[key]
	if s == nil {
		m.mu.Unlock()
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
	out := *s
	m.mu.Unlock()
	if len(changes) > 0 {
		m.emit(key, time.Now(), Event{Kind: "meta", Category: CatAudit, Level: LevelInfo, Actor: actor, Detail: strings.Join(changes, "；")})
	}
	return out, nil
}

func (m *Monitor) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.findLocked(id)
	key := ""
	if s != nil {
		key = s.ID
	}
	m.mu.Unlock()
	if key == "" {
		return errNotFound
	}
	if m.Hub.Online(key) {
		return esxi.ErrBad("system is online; stop the agent first")
	}
	if m.Store != nil {
		if err := m.Store.Delete(ctx, key); err != nil {
			return err
		}
	}
	m.mu.Lock()
	delete(m.systems, key)
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
