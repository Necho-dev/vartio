// Package monitor keeps probe-hub's fleet view: it polls online agents for metrics,
// derives rates from cumulative counters and persists history in PostgreSQL.
package systems

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"vartio/internal/common"
	"vartio/internal/entities"
	"vartio/internal/hub"
)

// System is one monitored machine, keyed by the agent's device fingerprint (ID).
// Agents before 1.8 have no fingerprint and use their BIOS UUID as ID.
type System struct {
	ID          string                `json:"id"`
	BiosUUID    string                `json:"biosUuid"`
	Device      *entities.DeviceFacts `json:"device,omitempty"`
	Name        string                `json:"name"`
	HostName    string                `json:"hostname"`
	Version     string                `json:"version"`
	RemoteAddr  string                `json:"remoteAddr"`
	Tags        []string              `json:"tags"`
	Online      bool                  `json:"online"`
	FirstSeen   time.Time             `json:"firstSeen"`
	LastSeen    time.Time             `json:"lastSeen"`
	ConnectedAt time.Time             `json:"connectedAt"`
	Info        entities.SysInfo      `json:"info"`
	Latest      *Sample               `json:"latest,omitempty"`
	// Caps and Runner come from the live session only.
	Caps   []string `json:"caps,omitempty"`
	Runner string   `json:"runner,omitempty"`
	// Credential is the hub's device credential state: active, pending, none,
	// or "" when the hub runs without device credentials.
	Credential string `json:"credential,omitempty"`
}

type Sample struct {
	TS           time.Time `json:"ts"`
	CPUPct       float64   `json:"cpuPct"`
	CPUUserPct   *float64  `json:"cpuUserPct,omitempty"`
	CPUSystemPct *float64  `json:"cpuSystemPct,omitempty"`
	CPUWaitPct   *float64  `json:"cpuWaitPct,omitempty"`
	// Cores is the live per-processor reading. It is not kept in history.
	Cores        []entities.CPUCore `json:"cores,omitempty"`
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
	// Bytes moved since the previous sample, for exact traffic totals.
	NetRxBytes uint64 `json:"netRxBytes,omitempty"`
	NetTxBytes uint64 `json:"netTxBytes,omitempty"`
	// The rest need agent >= 1.14.0; nil when unknown. NetErrs/NetDrops are
	// counts since the previous sample.
	NetRxPps   *float64           `json:"netRxPps,omitempty"`
	NetTxPps   *float64           `json:"netTxPps,omitempty"`
	NetErrs    *uint64            `json:"netErrs,omitempty"`
	NetDrops   *uint64            `json:"netDrops,omitempty"`
	TCP        *entities.TCPStats `json:"tcp,omitempty"`
	RetransPct *float64           `json:"retransPct,omitempty"`
	// Segments (bytes on macOS) sent / retransmitted since the previous sample,
	// so history can weight the ratio over longer windows.
	TCPOutSegs     *uint64               `json:"tcpOutSegs,omitempty"`
	TCPRetransSegs *uint64               `json:"tcpRetransSegs,omitempty"`
	Load           [3]float64            `json:"load"`
	TempMax        float64               `json:"tempMax,omitempty"`
	Procs          int                   `json:"procs"`
	LatencyMs      float64               `json:"latencyMs"`
	Disks          []entities.DiskUsage  `json:"disks,omitempty"`
	Temps          []entities.TempSensor `json:"temps,omitempty"`
	GPUs           []entities.GPUInfo    `json:"gpus,omitempty"`
	Battery        *entities.Battery     `json:"battery,omitempty"`
	NICs           []entities.NICInfo    `json:"nics,omitempty"`
	// NetNICs is per-interface throughput (nil until two samples land).
	NetNICs []NICNet      `json:"netNics,omitempty"`
	Docker  *DockerSample `json:"docker,omitempty"`
}

// NICNet is one physical NIC's rates for the latest sample and for history.
type NICNet struct {
	Name    string  `json:"name"`
	RxBps   float64 `json:"rxBps"`
	TxBps   float64 `json:"txBps"`
	RxBytes uint64  `json:"rxBytes,omitempty"`
	TxBytes uint64  `json:"txBytes,omitempty"`
}

type DockerSample struct {
	Engine     string                 `json:"engine,omitempty"`
	Version    string                 `json:"version,omitempty"`
	Running    int                    `json:"running"`
	Total      int                    `json:"total"`
	Error      string                 `json:"error,omitempty"`
	Containers []ContainerSample      `json:"containers"`
	Images     []entities.DockerImage `json:"images,omitempty"`
}

// ContainerSample rates are nil until a container has two samples in a row.
// CPUPct follows `docker stats`: 100 is one core.
type ContainerSample struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	State       string   `json:"state"`
	Status      string   `json:"status,omitempty"`
	Health      string   `json:"health,omitempty"`
	Project     string   `json:"project,omitempty"`
	Service     string   `json:"service,omitempty"`
	Compose     string   `json:"compose,omitempty"`
	Created     int64    `json:"created,omitempty"`
	Ports       string   `json:"ports,omitempty"`
	Update      bool     `json:"update,omitempty"`
	Stats       bool     `json:"stats,omitempty"`
	CPUPct      *float64 `json:"cpuPct,omitempty"`
	MemUsed     uint64   `json:"memUsed,omitempty"`
	MemLimit    uint64   `json:"memLimit,omitempty"`
	NetRxBps    *float64 `json:"netRxBps,omitempty"`
	NetTxBps    *float64 `json:"netTxBps,omitempty"`
	BlkReadBps  *float64 `json:"blkReadBps,omitempty"`
	BlkWriteBps *float64 `json:"blkWriteBps,omitempty"`
	Pids        int      `json:"pids,omitempty"`
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
	pk                  *entities.NetPackets
	tcp                 *entities.TCPStats
	ctr                 map[string]entities.ContainerStat
	nic                 map[string][2]uint64
}

// netErrEvery throttles "net.errors" events per system.
const netErrEvery = time.Hour

// stallAfter is how many polls in a row must fail before the gap is recorded.
// One slow sample is ordinary; three is about half a minute without metrics.
const stallAfter = 3

// stall tracks consecutive failed polls of one online system.
type stall struct {
	since  time.Time
	misses int
	logged bool
}

// minRetransBase is the fewest segments (bytes on macOS) sent in one interval
// for which a retransmission ratio is reported.
const minRetransBase = 200

type Monitor struct {
	Hub       *hub.Hub
	Store     *Store // nil: live view only, no history
	Interval  time.Duration
	Retention time.Duration // raw samples
	// Rollup tiers keep long ranges cheap; see PickSource.
	Retention5m, Retention1h time.Duration

	rollupMu sync.Mutex
	mu       sync.Mutex
	systems  map[string]*System
	prev     map[string]counters
	polling  map[string]bool
	rejected map[string]time.Time
	netErrAt map[string]time.Time
	stalls   map[string]*stall
	// sampleAt is the agent timestamp of the last accepted snapshot.
	sampleAt map[string]int64
	// Prune bookkeeping is touched only from Run. pruneLast is the previous
	// hour's raw deletion count; pruneDay* sums the calendar day for one line.
	pruneLast     int64
	pruneDay      string
	pruneDayN     int64
	pruneDayHours int64
}

func New(hub *hub.Hub, store *Store, interval, retention time.Duration) *Monitor {
	if interval < 2*time.Second {
		interval = 10 * time.Second
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	m := &Monitor{
		Hub: hub, Store: store, Interval: interval, Retention: retention,
		Retention5m: max(retention, 14*24*time.Hour), Retention1h: 365 * 24 * time.Hour,
		systems:  map[string]*System{},
		prev:     map[string]counters{},
		polling:  map[string]bool{},
		rejected: map[string]time.Time{},
		netErrAt: map[string]time.Time{},
		stalls:   map[string]*stall{},
		sampleAt: map[string]int64{},
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
	rollup := time.NewTicker(time.Minute)
	defer rollup.Stop()
	m.prune(ctx)
	go m.backfill(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, s := range m.Hub.List() {
				go m.poll(ctx, s.DeviceID)
			}
		case <-rollup.C:
			// Covers the hour that just closed so the hourly tier sees its last 5-minute bucket.
			m.rollup(ctx, time.Now().Add(-70*time.Minute))
		case <-prune.C:
			m.prune(ctx)
		}
	}
}

// backfill rolls up raw samples recorded since the last rollup (all of them
// on first start after an upgrade).
func (m *Monitor) backfill(ctx context.Context) {
	if m.Store == nil {
		return
	}
	since := time.Now().Add(-m.Retention)
	if last, err := m.Store.LastRollup(ctx); err != nil {
		log.Printf("monitor rollup: %v", err)
		return
	} else if last.After(since) {
		since = last.Add(-time.Hour)
	}
	start := time.Now()
	m.rollup(ctx, since)
	if d := time.Since(start); d > time.Second {
		log.Printf("monitor rollup: backfilled from %s in %s", since.Format(time.DateTime), d.Round(time.Millisecond))
	}
}

func (m *Monitor) rollup(ctx context.Context, since time.Time) {
	if m.Store == nil {
		return
	}
	// Concurrent upserts over the same buckets can deadlock.
	m.rollupMu.Lock()
	defer m.rollupMu.Unlock()
	c, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := m.Store.Rollup(c, since, m.Interval); err != nil {
		log.Printf("monitor rollup: %v", err)
	}
}

func (m *Monitor) prune(ctx context.Context) {
	if m.Store == nil {
		return
	}
	now := time.Now()
	cutoff := now.Add(-m.Retention)
	n, err := m.Store.Prune(ctx, cutoff, now.Add(-30*24*time.Hour))
	if err != nil {
		log.Printf("monitor prune: %v", err)
	} else if n > 0 {
		m.logPrune(now, cutoff, n)
	}
	if err := m.Store.PruneRollups(ctx, now.Add(-m.Retention5m), now.Add(-m.Retention1h)); err != nil {
		log.Printf("monitor prune rollups: %v", err)
	}
}

// logPrune records one hour of retention deletion. A steady fleet drops about
// the same number of raw samples every hour, so that case is folded into one
// line per calendar day. The hour is logged on its own when the count moves.
func (m *Monitor) logPrune(now, cutoff time.Time, n int64) {
	per := samplesPerHostHour(m.Interval)
	today := now.Format(time.DateOnly)
	if m.pruneDay == "" {
		m.pruneDay = today
	} else if today != m.pruneDay {
		if m.pruneDayN > 0 {
			log.Printf("%s", pruneDayMessage(m.pruneDay, m.pruneDayN, m.pruneDayHours, per, m.Retention, m.Interval))
		}
		m.pruneDay, m.pruneDayN, m.pruneDayHours = today, 0, 0
	}
	if pruneNotable(n, m.pruneLast, per) {
		log.Printf("%s", pruneHourMessage(n, m.pruneLast, per, cutoff, m.Retention, m.Interval))
	}
	m.pruneLast = n
	m.pruneDayN += n
	m.pruneDayHours++
}

func samplesPerHostHour(interval time.Duration) int64 {
	if interval <= 0 {
		return 0
	}
	n := int64(time.Hour / interval)
	if n < 1 {
		return 1
	}
	return n
}

// pruneNotable is true for the first deletion and when the count moves by at
// least half a host-hour or 10%. Smaller jitter is the same retention hour.
func pruneNotable(n, prev, perHost int64) bool {
	if prev <= 0 {
		return n > 0
	}
	d := n - prev
	if d < 0 {
		d = -d
	}
	if perHost > 0 && d*2 >= perHost {
		return true
	}
	return d*10 >= prev
}

func pruneHourMessage(n, prev, perHost int64, cutoff time.Time, keep, every time.Duration) string {
	was := ""
	if prev > 0 && prev != n {
		was = fmt.Sprintf(" (was %d)", prev)
	}
	return fmt.Sprintf("monitor prune: deleted %d raw samples%s before %s (keep %s, ~%s hosts at %s)",
		n, was, cutoff.Format(time.DateTime), fmtKeep(keep), fmtHosts(n, perHost), every)
}

func pruneDayMessage(day string, n, hours, perHost int64, keep, every time.Duration) string {
	denom := perHost * hours
	return fmt.Sprintf("monitor prune: %s deleted %d raw samples over %dh (keep %s, ~%s hosts at %s)",
		day, n, hours, fmtKeep(keep), fmtHosts(n, denom), every)
}

func fmtKeep(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return d.String()
}

func fmtHosts(samples, perHost int64) string {
	if perHost <= 0 {
		return "?"
	}
	h := float64(samples) / float64(perHost)
	if math.Abs(h-math.Round(h)) < 0.05 {
		return fmt.Sprintf("%d", int(math.Round(h)))
	}
	return fmt.Sprintf("%.1f", h)
}

func (m *Monitor) onConnect(info entities.SessionInfo) {
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
		detail := sys.Version + " → " + info.Version
		if u := info.Upgrade; u != nil && u.OK && u.To == info.Version {
			detail += upgradeMode(u.Mode)
		}
		events = append(events, Event{Kind: "upgrade", Category: CatLifecycle, Level: LevelInfo, Detail: detail})
	}
	if u := info.Upgrade; u != nil && !u.OK {
		events = append(events, Event{Kind: "upgrade.failed", Category: CatLifecycle, Level: LevelWarn,
			Detail: u.From + " → " + u.To + upgradeMode(u.Mode) + " 未成功，已回滚：" + u.Error})
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
	sys.Caps, sys.Runner = info.Caps, info.Runner
	sys.Online = true
	sys.ConnectedAt = at
	sys.LastSeen = at
	snap := *sys
	delete(m.prev, key)
	delete(m.stalls, key)
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
func (m *Monitor) adoptLocked(info entities.SessionInfo) (*System, string) {
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

func changedPart(a, b *entities.DeviceFacts) string {
	switch {
	case a.MAC != b.MAC:
		return "主网卡 MAC 变化"
	case a.HW != b.HW:
		return "硬件 UUID 变化"
	default:
		return "系统安装 ID 变化"
	}
}

func (m *Monitor) onDisconnect(info entities.SessionInfo) {
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
	if st := m.stalls[key]; st != nil && st.logged {
		detail = strings.TrimPrefix(detail+"；断开前指标采样已中断 "+now.Sub(st.since).Round(time.Second).String(), "；")
	}
	delete(m.prev, key)
	delete(m.stalls, key)
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
func (m *Monitor) onReject(hello entities.Hello, remote, code, _ string) {
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
	if !known {
		return
	}
	e := Event{Kind: "conflict", Category: CatLifecycle, Level: LevelWarn, IP: remote, Version: hello.Version,
		Detail: "同指纹设备重复接入已拒绝（对方主机名 " + hello.HostName + "，BIOS " + hello.BiosUUID + "）"}
	if code != "device_conflict" {
		e.Kind = "auth.reject"
		e.Detail = "设备凭据校验失败，已拒绝接入（" + code + "，对方主机名 " + hello.HostName + "）；探针重装丢失凭据时可在详情页重置凭据"
		if code == "enrollment_disabled" {
			e.Detail = "hub 已关闭新设备登记（PROBE_HUB_ENROLL=off），已拒绝无凭据接入（对方主机名 " + hello.HostName + "）"
		}
	}
	m.emit(key, now, e)
}

func (m *Monitor) onAudit(a hub.Audit) {
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

	c, cancel := context.WithTimeout(ctx, entities.ListTimeout)
	defer cancel()
	start := time.Now()
	met, err := m.Hub.Metrics(c, id)
	if isNotImplemented(err) {
		met, err = m.legacy(c, id)
	}
	if err != nil {
		log.Printf("monitor poll %s: %v", id, err)
		m.pollFailed(id, start, err)
		return
	}
	if m.repeated(id, met.TimeMs) {
		err := fmt.Errorf("Agent 采样未完成，返回的仍是 %s 的数据", time.UnixMilli(met.TimeMs).Format(time.TimeOnly))
		log.Printf("monitor poll %s: %v", id, err)
		m.pollFailed(id, start, err)
		return
	}
	latency := time.Since(start)
	if rtt, ok := m.Hub.RTT(id); ok {
		latency = rtt
	}
	scrubInfo(&met.Info)
	smp := m.derive(id, met, latency)
	scrubSample(&smp)
	entities.Finite(&smp)

	m.mu.Lock()
	sys := m.systems[id]
	if sys == nil {
		m.mu.Unlock()
		return
	}
	events := diff(sys.Info, sys.Latest, met.Info, &smp)
	if smp.NetErrs != nil && *smp.NetErrs > 0 && time.Since(m.netErrAt[id]) > netErrEvery {
		m.netErrAt[id] = time.Now()
		events = append(events, Event{Kind: "net.errors", Category: CatLifecycle, Level: LevelWarn,
			Detail: fmt.Sprintf("网卡收发错误 +%d（%s 内）；1 小时内不再重复提醒", *smp.NetErrs, m.Interval)})
	}
	sys.Latest = &smp
	sys.Info = met.Info
	if met.Info.HostName != "" {
		sys.HostName = met.Info.HostName
	}
	sys.LastSeen = smp.TS
	if e, ok := m.recoveredLocked(id, time.Now()); ok {
		events = append(events, e)
	}
	m.mu.Unlock()
	if m.Store != nil {
		if err := m.Store.Record(context.Background(), id, met.Info, smp); err != nil {
			log.Printf("monitor record %s: %v", id, err)
		}
	}
	m.emit(id, smp.TS, events...)
}

// repeated reports a snapshot the agent already returned. An agent whose
// collection is stuck answers with its previous snapshot; storing it again
// would draw a flat line and zero rates instead of a gap.
func (m *Monitor) repeated(id string, timeMs int64) bool {
	if timeMs == 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sampleAt[id] == timeMs {
		return true
	}
	m.sampleAt[id] = timeMs
	return false
}

// pollFailed counts a poll that returned no metrics. The third miss in a row is
// recorded once; an agent that is gone is reported by its offline event instead.
func (m *Monitor) pollFailed(id string, at time.Time, err error) {
	var ce *common.Error
	if errors.As(err, &ce) && ce.Code == common.ErrOffline().Code {
		return
	}
	m.mu.Lock()
	if sys := m.systems[id]; sys == nil || !sys.Online {
		m.mu.Unlock()
		return
	}
	st := m.stalls[id]
	if st == nil {
		st = &stall{since: at}
		m.stalls[id] = st
	}
	st.misses++
	report := !st.logged && st.misses >= stallAfter
	if report {
		st.logged = true
	}
	since, misses := st.since, st.misses
	m.mu.Unlock()
	if !report {
		return
	}
	reason := err.Error()
	if errors.As(err, &ce) && ce.Code == "timeout" {
		reason = fmt.Sprintf("%s 内未返回", entities.ListTimeout)
	}
	log.Printf("monitor stall %s: %d polls failed since %s", id, misses, since.Format(time.TimeOnly))
	m.emit(id, at, Event{Kind: "poll.stall", Category: CatLifecycle, Level: LevelWarn,
		Detail: fmt.Sprintf("指标采样连续 %d 次失败（自 %s 起，%s）", misses, since.Format(time.TimeOnly), reason)})
}

// recoveredLocked closes a run of failed polls. Only a run that was recorded
// gets a recovery event. The caller holds m.mu.
func (m *Monitor) recoveredLocked(id string, at time.Time) (Event, bool) {
	st := m.stalls[id]
	if st == nil {
		return Event{}, false
	}
	delete(m.stalls, id)
	if !st.logged {
		return Event{}, false
	}
	gap := at.Sub(st.since).Round(time.Second)
	log.Printf("monitor recovered %s after %s, %d polls missed", id, gap, st.misses)
	return Event{Kind: "poll.recover", Category: CatLifecycle, Level: LevelInfo,
		Detail: fmt.Sprintf("指标采样恢复，中断 %s，缺 %d 次采样", gap, st.misses)}, true
}

func (m *Monitor) derive(id string, met entities.Metrics, latency time.Duration) Sample {
	now := time.Now()
	smp := Sample{
		TS: now.UTC().Truncate(time.Second), CPUPct: met.CPUPct,
		CPUUserPct: met.CPUUserPct, CPUSystemPct: met.CPUSystemPct, CPUWaitPct: met.CPUWaitPct, Cores: met.Cores,
		MemUsed: met.MemUsed, MemTotal: met.MemTotal,
		SwapUsed: met.SwapUsed, SwapTotal: met.SwapTotal,
		Load: met.Load, Procs: met.Procs,
		LatencyMs: math.Round(float64(latency.Microseconds())/10) / 100,
		Temps:     met.Temps, GPUs: met.GPUs, Battery: met.Battery, NICs: met.NICs,
	}
	// RootMount only labels the system disk. The headline percentage is every
	// partition after shared pools (APFS, one device mounted twice) are collapsed.
	smp.RootMount = entities.CapacityMount(met.Disks, met.RootMount)
	smp.Disks = dedupePools(met.Disks, smp.RootMount)
	if met.MemTotal > 0 {
		smp.MemPct = float64(met.MemUsed) / float64(met.MemTotal) * 100
	}
	smp.DiskUsed, smp.DiskTotal = entities.DiskSum(smp.Disks)
	if smp.DiskTotal > 0 {
		smp.DiskPct = float64(smp.DiskUsed) / float64(smp.DiskTotal) * 100
	}
	for _, t := range met.Temps {
		if t.C > smp.TempMax {
			smp.TempMax = t.C
		}
	}
	smp.TCP = met.TCP
	cur := counters{at: now, read: met.DiskRead, write: met.DiskWrite, rx: met.NetRx, tx: met.NetTx, pk: met.NetPkts, tcp: met.TCP, nic: nicCounters(met.NICs)}
	if d := met.Docker; d != nil {
		cur.ctr = make(map[string]entities.ContainerStat, len(d.Containers))
		for _, c := range d.Containers {
			if c.Stats {
				cur.ctr[c.ID] = c
			}
		}
	}
	m.mu.Lock()
	prev, ok := m.prev[id]
	m.prev[id] = cur
	m.mu.Unlock()
	dt := cur.at.Sub(prev.at).Seconds()
	if !ok || dt <= 0.5 {
		dt = 0
	}
	smp.Docker = dockerSample(met.Docker, prev.ctr, dt)
	if dt == 0 {
		return smp
	}
	smp.DiskReadBps = rate(prev.read, cur.read, dt)
	smp.DiskWriteBps = rate(prev.write, cur.write, dt)
	smp.NetRxBps = rate(prev.rx, cur.rx, dt)
	smp.NetTxBps = rate(prev.tx, cur.tx, dt)
	smp.NetRxBytes, smp.NetTxBytes = delta(prev.rx, cur.rx), delta(prev.tx, cur.tx)
	smp.NetNICs = nicRates(prev.nic, cur.nic, dt)
	if p, c := prev.pk, cur.pk; p != nil && c != nil {
		rx, tx := rate(p.Rx, c.Rx, dt), rate(p.Tx, c.Tx, dt)
		errs := delta(p.ErrIn, c.ErrIn) + delta(p.ErrOut, c.ErrOut)
		drops := delta(p.DropIn, c.DropIn) + delta(p.DropOut, c.DropOut)
		smp.NetRxPps, smp.NetTxPps, smp.NetErrs, smp.NetDrops = &rx, &tx, &errs, &drops
	}
	// Counters that went backwards (wrap, or nettop losing exited processes on
	// macOS) say nothing about this interval.
	if p, c := prev.tcp, cur.tcp; p != nil && c != nil && c.OutSegs >= p.OutSegs && c.Retrans >= p.Retrans {
		out, re := c.OutSegs-p.OutSegs, c.Retrans-p.Retrans
		smp.TCPOutSegs, smp.TCPRetransSegs = &out, &re
		// Below minRetransBase sent units a single retransmit swings the ratio to tens of percent.
		if out >= minRetransBase {
			r := math.Min(100, float64(re)/float64(out)*100)
			smp.RetransPct = &r
		}
	}
	return smp
}

// maxContainerBps (100 Gbit/s) bounds believable container network and disk rates.
const maxContainerBps = 12.5e9

// dockerSample derives per-container rates from the previous sample's counters
// (dt == 0: no usable previous sample).
func dockerSample(d *entities.DockerInfo, prev map[string]entities.ContainerStat, dt float64) *DockerSample {
	if d == nil {
		return nil
	}
	out := &DockerSample{Engine: d.Engine, Version: d.Version, Running: d.Running, Total: d.Total, Error: d.Error,
		Containers: make([]ContainerSample, 0, len(d.Containers)), Images: d.Images}
	for _, c := range d.Containers {
		cs := ContainerSample{ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, Status: c.Status, Health: c.Health,
			Project: c.Project, Service: c.Service, Compose: c.Compose, Created: c.Created, Ports: c.Ports, Update: c.Update, Stats: c.Stats, MemUsed: c.MemUsed, MemLimit: c.MemLimit, Pids: c.Pids}
		if p, ok := prev[c.ID]; ok && c.Stats && dt > 0 && c.CPUNs >= p.CPUNs {
			wall := dt
			if c.ReadNs > p.ReadNs && p.ReadNs > 0 {
				wall = float64(c.ReadNs-p.ReadNs) / 1e9
			}
			var cpu float64
			if dSys := float64(c.SysNs) - float64(p.SysNs); c.SysNs > 0 && dSys > 0 {
				cpu = float64(c.CPUNs-p.CPUNs) / dSys * float64(max(c.CPUs, 1)) * 100
			} else {
				cpu = float64(c.CPUNs-p.CPUNs) / (wall * 1e9) * 100
			}
			cpu = math.Round(cpu*100) / 100
			rx, tx := rate(p.NetRx, c.NetRx, wall), rate(p.NetTx, c.NetTx, wall)
			br, bw := rate(p.BlkRead, c.BlkRead, wall), rate(p.BlkWrite, c.BlkWrite, wall)
			// A counter glitch shows up as an impossible rate; skip the sample.
			if cpu <= float64(max(c.CPUs, 1))*110 && max(rx, tx, br, bw) <= maxContainerBps {
				cs.CPUPct, cs.NetRxBps, cs.NetTxBps, cs.BlkReadBps, cs.BlkWriteBps = &cpu, &rx, &tx, &br, &bw
			}
		}
		out.Containers = append(out.Containers, cs)
	}
	return out
}

func nicCounters(nics []entities.NICInfo) map[string][2]uint64 {
	if len(nics) == 0 {
		return nil
	}
	out := make(map[string][2]uint64, len(nics))
	for _, n := range nics {
		if n.Rx == 0 && n.Tx == 0 {
			continue
		}
		out[n.Name] = [2]uint64{n.Rx, n.Tx}
	}
	return out
}

func nicRates(prev, cur map[string][2]uint64, dt float64) []NICNet {
	if dt == 0 || len(cur) == 0 {
		return nil
	}
	var out []NICNet
	for name, c := range cur {
		p, ok := prev[name]
		if !ok {
			continue
		}
		out = append(out, NICNet{Name: name, RxBps: rate(p[0], c[0], dt), TxBps: rate(p[1], c[1], dt),
			RxBytes: delta(p[0], c[0]), TxBytes: delta(p[1], c[1])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func delta(a, b uint64) uint64 {
	if b < a {
		return 0
	}
	return b - a
}

// legacy maps a pre-1.2 agent's health reply onto Metrics so old agents still show up.
func (m *Monitor) legacy(ctx context.Context, id string) (entities.Metrics, error) {
	h, err := m.Hub.Health(ctx, id)
	if err != nil {
		return entities.Metrics{}, err
	}
	out := entities.Metrics{
		Info:     entities.SysInfo{CPUModel: h.CPU.Model, Threads: h.CPU.LogicalCPUs},
		CPUPct:   h.CPU.Pct,
		MemTotal: uint64(h.Memory.TotalMB) << 20,
		MemUsed:  uint64(h.Memory.TotalMB-h.Memory.AvailMB) << 20,
		NICs:     h.NICs,
	}
	for _, d := range h.Disks {
		out.Disks = append(out.Disks, entities.DiskUsage{Mount: d.Name, Total: uint64(d.TotalGB * 1e9), Used: uint64(d.UsedGB * 1e9)})
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
	var api *common.Error
	return errors.As(err, &api) && api.Code == "not_implemented"
}

func (m *Monitor) List() []System {
	m.mu.Lock()
	out := make([]System, 0, len(m.systems))
	for _, s := range m.systems {
		out = append(out, *s)
	}
	m.mu.Unlock()
	for i := range out {
		out[i].Credential = m.Hub.CredentialState(out[i].ID)
	}
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
	s := m.findLocked(id)
	if s == nil {
		m.mu.Unlock()
		return System{}, false
	}
	out := *s
	m.mu.Unlock()
	out.Credential = m.Hub.CredentialState(out.ID)
	return out, true
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
		return common.ErrBad("system is online; stop the agent first")
	}
	if m.Store != nil {
		if err := m.Store.Delete(ctx, key); err != nil {
			return err
		}
	}
	m.mu.Lock()
	delete(m.systems, key)
	m.mu.Unlock()
	m.Hub.ForgetCredential(key)
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

func upgradeMode(mode string) string {
	switch mode {
	case "auto":
		return "（自动更新）"
	case "push":
		return "（远程升级）"
	}
	return ""
}
