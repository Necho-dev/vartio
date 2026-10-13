package systems

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
	"vartio/internal/entities"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schema = `
CREATE TABLE IF NOT EXISTS probe_systems (
	device_id     text PRIMARY KEY,
	bios_uuid     text NOT NULL DEFAULT '',
	name          text NOT NULL DEFAULT '',
	hostname      text NOT NULL DEFAULT '',
	agent_version text NOT NULL DEFAULT '',
	remote_addr   text NOT NULL DEFAULT '',
	tags          text[] NOT NULL DEFAULT '{}',
	info          jsonb NOT NULL DEFAULT '{}',
	latest        jsonb,
	online        boolean NOT NULL DEFAULT false,
	first_seen    timestamptz NOT NULL DEFAULT now(),
	last_seen     timestamptz NOT NULL DEFAULT now(),
	connected_at  timestamptz,
	device        jsonb
);
CREATE TABLE IF NOT EXISTS probe_metrics (
	device_id      text NOT NULL REFERENCES probe_systems(device_id) ON DELETE CASCADE ON UPDATE CASCADE,
	ts             timestamptz NOT NULL,
	cpu_pct        real NOT NULL,
	mem_used       bigint NOT NULL,
	mem_total      bigint NOT NULL,
	swap_used      bigint NOT NULL,
	swap_total     bigint NOT NULL,
	disk_used      bigint NOT NULL,
	disk_total     bigint NOT NULL,
	disk_read_bps  double precision NOT NULL,
	disk_write_bps double precision NOT NULL,
	net_rx_bps     double precision NOT NULL,
	net_tx_bps     double precision NOT NULL,
	load1          real NOT NULL DEFAULT 0,
	load5          real,
	load15         real,
	temp_max       real,
	procs          integer NOT NULL DEFAULT 0,
	disk_parts     jsonb,
	gpus           jsonb,
	temps          jsonb,
	PRIMARY KEY (device_id, ts)
);
CREATE TABLE IF NOT EXISTS probe_events (
	id        bigserial PRIMARY KEY,
	device_id text NOT NULL REFERENCES probe_systems(device_id) ON DELETE CASCADE ON UPDATE CASCADE,
	ts        timestamptz NOT NULL DEFAULT now(),
	kind          text NOT NULL,
	category      text NOT NULL DEFAULT 'lifecycle',
	level         text NOT NULL DEFAULT 'info',
	ip            text NOT NULL DEFAULT '',
	agent_version text NOT NULL DEFAULT '',
	actor         text NOT NULL DEFAULT '',
	detail        text NOT NULL DEFAULT ''
);
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS load5 real;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS load15 real;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS net_rx_bytes bigint;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS net_tx_bytes bigint;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS net_rx_pps double precision;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS net_tx_pps double precision;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS net_errs bigint;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS net_drops bigint;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS tcp_estab integer;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS tcp_time_wait integer;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS tcp_listen integer;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS retrans_pct real;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS latency_ms real;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS tcp_out_segs bigint;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS tcp_retrans_segs bigint;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS conn_tcp integer;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS conn_udp integer;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS 	conn_other integer;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS net_nics jsonb;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS gpus jsonb;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS temps jsonb;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS cpu_user real;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS cpu_system real;
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS cpu_iowait real;
CREATE INDEX IF NOT EXISTS probe_events_device_ts ON probe_events (device_id, ts DESC);
CREATE TABLE IF NOT EXISTS probe_container_metrics (
	device_id     text NOT NULL REFERENCES probe_systems(device_id) ON DELETE CASCADE ON UPDATE CASCADE,
	ts            timestamptz NOT NULL,
	name          text NOT NULL,
	cpu_pct       real,
	mem_used      bigint NOT NULL,
	mem_limit     bigint NOT NULL,
	net_rx_bps    double precision,
	net_tx_bps    double precision,
	blk_read_bps  double precision,
	blk_write_bps double precision,
	pids          integer,
	PRIMARY KEY (device_id, ts, name)
);
ALTER TABLE probe_container_metrics ADD COLUMN IF NOT EXISTS pids integer;
` + rollupSchema + `
ALTER TABLE probe_container_metrics_5m ADD COLUMN IF NOT EXISTS pids double precision;
ALTER TABLE probe_container_metrics_1h ADD COLUMN IF NOT EXISTS pids double precision;
ALTER TABLE probe_metrics_5m ADD COLUMN IF NOT EXISTS net_nics jsonb;
ALTER TABLE probe_metrics_1h ADD COLUMN IF NOT EXISTS net_nics jsonb;
ALTER TABLE probe_metrics_5m ADD COLUMN IF NOT EXISTS gpus jsonb;
ALTER TABLE probe_metrics_1h ADD COLUMN IF NOT EXISTS gpus jsonb;
ALTER TABLE probe_metrics_5m ADD COLUMN IF NOT EXISTS temps jsonb;
ALTER TABLE probe_metrics_1h ADD COLUMN IF NOT EXISTS temps jsonb;
ALTER TABLE probe_metrics_5m ADD COLUMN IF NOT EXISTS cpu_user double precision;
ALTER TABLE probe_metrics_5m ADD COLUMN IF NOT EXISTS cpu_system double precision;
ALTER TABLE probe_metrics_5m ADD COLUMN IF NOT EXISTS cpu_iowait double precision;
ALTER TABLE probe_metrics_1h ADD COLUMN IF NOT EXISTS cpu_user double precision;
ALTER TABLE probe_metrics_1h ADD COLUMN IF NOT EXISTS cpu_system double precision;
ALTER TABLE probe_metrics_1h ADD COLUMN IF NOT EXISTS cpu_iowait double precision;
`

// Store persists systems, samples and events in PostgreSQL.
type Store struct {
	db *pgxpool.Pool
}

func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(ctx, schema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(ctx, `
		WITH was AS (UPDATE probe_systems SET online = false WHERE online
		             RETURNING device_id, last_seen, remote_addr, agent_version)
		INSERT INTO probe_events (device_id, ts, kind, category, level, ip, agent_version, detail)
		SELECT device_id, last_seen, 'offline', 'lifecycle', 'info', remote_addr, agent_version, 'hub 重启，会话中断' FROM was`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() { s.db.Close() }

func (s *Store) LoadSystems(ctx context.Context) ([]*System, error) {
	rows, err := s.db.Query(ctx, `
		SELECT device_id, bios_uuid, name, hostname, agent_version, remote_addr, tags, info, latest,
		       device, first_seen, last_seen, connected_at
		FROM probe_systems`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*System
	for rows.Next() {
		var sys System
		var info, latest, device []byte
		var connected *time.Time
		if err := rows.Scan(&sys.ID, &sys.BiosUUID, &sys.Name, &sys.HostName, &sys.Version, &sys.RemoteAddr,
			&sys.Tags, &info, &latest, &device, &sys.FirstSeen, &sys.LastSeen, &connected); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(info, &sys.Info)
		if len(device) > 0 {
			var d entities.DeviceFacts
			if json.Unmarshal(device, &d) == nil {
				sys.Device = &d
			}
		}
		if len(latest) > 0 {
			var smp Sample
			if json.Unmarshal(latest, &smp) == nil {
				sys.Latest = &smp
			}
		}
		if connected != nil {
			sys.ConnectedAt = *connected
		}
		if sys.Tags == nil {
			sys.Tags = []string{}
		}
		if sys.Name == "" {
			sys.Name = sys.HostName
		}
		out = append(out, &sys)
	}
	return out, rows.Err()
}

func (s *Store) Connect(ctx context.Context, sys System) error {
	var device []byte
	if sys.Device != nil {
		device = postgresJSON(mustJSON(sys.Device))
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO probe_systems (device_id, bios_uuid, name, hostname, agent_version, remote_addr, device, online, first_seen, last_seen, connected_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8, $8, $8)
		ON CONFLICT (device_id) DO UPDATE SET
			bios_uuid = EXCLUDED.bios_uuid, hostname = EXCLUDED.hostname, agent_version = EXCLUDED.agent_version,
			remote_addr = EXCLUDED.remote_addr, device = COALESCE(EXCLUDED.device, probe_systems.device), online = true,
			last_seen = EXCLUDED.last_seen, connected_at = EXCLUDED.connected_at`,
		sys.ID, scrub(sys.BiosUUID), scrub(sys.Name), scrub(sys.HostName), scrub(sys.Version), scrub(sys.RemoteAddr), device, sys.ConnectedAt)
	return err
}

// Rekey moves a system (and, by cascade, its metrics and events) to a new device id.
func (s *Store) Rekey(ctx context.Context, from, to string) error {
	_, err := s.db.Exec(ctx, `UPDATE probe_systems SET device_id = $2 WHERE device_id = $1`, from, to)
	return err
}

func (s *Store) Disconnect(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE probe_systems SET online = false, last_seen = $2 WHERE device_id = $1`, id, at)
	return err
}

func (s *Store) AddEvent(ctx context.Context, id string, e Event) error {
	_, err := s.db.Exec(ctx, `INSERT INTO probe_events (device_id, ts, kind, category, level, ip, agent_version, actor, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, e.TS, scrub(e.Kind), scrub(e.Category), scrub(e.Level), scrub(e.IP), scrub(e.Version), scrub(e.Actor), scrub(e.Detail))
	return err
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// postgresJSON drops \u0000. PostgreSQL json/jsonb cannot store a null byte,
// and one such string would otherwise reject the whole metrics sample.
func postgresJSON(b []byte) []byte {
	nul := []byte(`\u0000`)
	if !bytes.Contains(b, nul) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if i+len(nul) <= len(b) && bytes.Equal(b[i:i+len(nul)], nul) && !jsonEscapePrefixed(b, i) {
			i += len(nul)
			continue
		}
		out = append(out, b[i])
		i++
	}
	return out
}

// jsonEscapePrefixed reports whether the backslash at i is itself escaped,
// so a literal \u0000 typed by a program is left alone.
func jsonEscapePrefixed(b []byte, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && b[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

func (s *Store) Record(ctx context.Context, id string, info entities.SysInfo, smp Sample) error {
	infoJSON := postgresJSON(mustJSON(info))
	latestJSON := postgresJSON(mustJSON(smp))
	b := &pgx.Batch{}
	b.Queue(`UPDATE probe_systems SET info = $2, latest = $3, last_seen = $4, hostname = COALESCE(NULLIF($5, ''), hostname) WHERE device_id = $1`,
		id, infoJSON, latestJSON, smp.TS, info.HostName)
	var temp *float64
	if smp.TempMax > 0 {
		temp = &smp.TempMax
	}
	var parts []byte
	if len(smp.Disks) > 0 {
		used := make(map[string]uint64, len(smp.Disks))
		for _, d := range smp.Disks {
			used[d.Mount] = d.Used
		}
		parts = postgresJSON(mustJSON(used))
	}
	var nics []byte
	if len(smp.NetNICs) > 0 {
		m := make(map[string]nicPoint, len(smp.NetNICs))
		for _, n := range smp.NetNICs {
			m[n.Name] = nicPoint{Rx: n.RxBps, Tx: n.TxBps, Rb: n.RxBytes, Tb: n.TxBytes}
		}
		nics = postgresJSON(mustJSON(m))
	}
	gpuJSON := postgresJSON(gpuUtilsJSON(smp.GPUs))
	tempJSON := postgresJSON(tempSensorsJSON(smp.Temps))
	var estab, tw, listen, cTCP, cUDP, cOther *int
	if t := smp.TCP; t != nil {
		estab, tw, listen = &t.Established, &t.TimeWait, &t.Listen
		if t.Total > 0 {
			cTCP, cUDP, cOther = &t.Total, &t.UDP, &t.Other
		}
	}
	b.Queue(`INSERT INTO probe_metrics (device_id, ts, cpu_pct, mem_used, mem_total, swap_used, swap_total,
			disk_used, disk_total, disk_read_bps, disk_write_bps, net_rx_bps, net_tx_bps, load1, load5, load15, temp_max, procs, disk_parts,
			net_rx_bytes, net_tx_bytes, net_rx_pps, net_tx_pps, net_errs, net_drops, tcp_estab, tcp_time_wait, tcp_listen, retrans_pct, latency_ms, tcp_out_segs, tcp_retrans_segs, conn_tcp, conn_udp, conn_other, net_nics, gpus, temps,
			cpu_user, cpu_system, cpu_iowait)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41) ON CONFLICT DO NOTHING`,
		id, smp.TS, smp.CPUPct, int64(smp.MemUsed), int64(smp.MemTotal), int64(smp.SwapUsed), int64(smp.SwapTotal),
		int64(smp.DiskUsed), int64(smp.DiskTotal), smp.DiskReadBps, smp.DiskWriteBps, smp.NetRxBps, smp.NetTxBps,
		smp.Load[0], smp.Load[1], smp.Load[2], temp, smp.Procs, parts,
		int64(smp.NetRxBytes), int64(smp.NetTxBytes), smp.NetRxPps, smp.NetTxPps, i64(smp.NetErrs), i64(smp.NetDrops),
		estab, tw, listen, smp.RetransPct, smp.LatencyMs, i64(smp.TCPOutSegs), i64(smp.TCPRetransSegs), cTCP, cUDP, cOther, nics, gpuJSON, tempJSON,
		smp.CPUUserPct, smp.CPUSystemPct, smp.CPUWaitPct)
	if d := smp.Docker; d != nil {
		for _, c := range d.Containers {
			if !c.Stats {
				continue
			}
			b.Queue(`INSERT INTO probe_container_metrics (device_id, ts, name, cpu_pct, mem_used, mem_limit, net_rx_bps, net_tx_bps, blk_read_bps, blk_write_bps, pids)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`,
				id, smp.TS, c.Name, c.CPUPct, int64(c.MemUsed), int64(c.MemLimit), c.NetRxBps, c.NetTxBps, c.BlkReadBps, c.BlkWriteBps, c.Pids)
		}
	}
	return s.db.SendBatch(ctx, b).Close()
}

// ContainerSeries holds per-container lines on shared buckets; nil = no sample.
type ContainerSeries struct {
	StepSec    int             `json:"stepSec"`
	T          []int64         `json:"t"`
	Containers []ContainerLine `json:"containers"`
}

type ContainerLine struct {
	Name     string     `json:"name"`
	MemLimit float64    `json:"memLimit"`
	CPU      []*float64 `json:"cpu"`
	Mem      []*float64 `json:"mem"`
	NetRx    []*float64 `json:"netRx"`
	NetTx    []*float64 `json:"netTx"`
	BlkRead  []*float64 `json:"blkRead"`
	BlkWrite []*float64 `json:"blkWrite"`
	Pids     []*float64 `json:"pids"`
}

// ContainerSeries returns the top containers by average CPU and by average
// memory (up to top of each) over the range.
func (s *Store) ContainerSeries(ctx context.Context, id string, src Source, from time.Time, step time.Duration, top int) (*ContainerSeries, error) {
	out := &ContainerSeries{StepSec: int(step / time.Second), T: []int64{}, Containers: []ContainerLine{}}
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		WITH ranked AS (
			SELECT name, rank() OVER (ORDER BY avg(cpu_pct) DESC NULLS LAST) AS rc, rank() OVER (ORDER BY avg(mem_used) DESC) AS rm
			FROM %[1]s WHERE device_id = $1 AND ts >= $2 GROUP BY name)
		SELECT date_bin(make_interval(secs => $3::float8), m.ts, TIMESTAMPTZ 'epoch') AS b, m.name,
			avg(m.cpu_pct)::float8, avg(m.mem_used)::float8, max(m.mem_limit)::float8,
			avg(m.net_rx_bps)::float8, avg(m.net_tx_bps)::float8, avg(m.blk_read_bps)::float8, avg(m.blk_write_bps)::float8,
			avg(m.pids)::float8
		FROM %[1]s m JOIN ranked r USING (name)
		WHERE m.device_id = $1 AND m.ts >= $2 AND (r.rc <= $4 OR r.rm <= $4)
		GROUP BY b, m.name ORDER BY b`, src.containers), id, from, step.Seconds(), top)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type point struct {
		i                                     int
		cpu, mem, limit, rx, tx, br, bw, pids *float64
	}
	byName := map[string][]point{}
	var order []string
	for rows.Next() {
		var t time.Time
		var name string
		var p point
		if err := rows.Scan(&t, &name, &p.cpu, &p.mem, &p.limit, &p.rx, &p.tx, &p.br, &p.bw, &p.pids); err != nil {
			return nil, err
		}
		ms := t.UnixMilli()
		if n := len(out.T); n == 0 || out.T[n-1] != ms {
			out.T = append(out.T, ms)
		}
		p.i = len(out.T) - 1
		if _, ok := byName[name]; !ok {
			order = append(order, name)
		}
		byName[name] = append(byName[name], p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	n := len(out.T)
	col := func() []*float64 { return make([]*float64, n) }
	for _, name := range order {
		l := ContainerLine{Name: name, CPU: col(), Mem: col(), NetRx: col(), NetTx: col(), BlkRead: col(), BlkWrite: col(), Pids: col()}
		for _, p := range byName[name] {
			l.CPU[p.i], l.Mem[p.i], l.NetRx[p.i], l.NetTx[p.i], l.BlkRead[p.i], l.BlkWrite[p.i], l.Pids[p.i] = p.cpu, p.mem, p.rx, p.tx, p.br, p.bw, p.pids
			if p.limit != nil && *p.limit > l.MemLimit {
				l.MemLimit = *p.limit
			}
		}
		out.Containers = append(out.Containers, l)
	}
	return out, nil
}

func (s *Store) UpdateMeta(ctx context.Context, id string, name *string, tags []string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE probe_systems SET
			name = COALESCE($2, name),
			tags = COALESCE($3, tags)
		WHERE device_id = $1`, id, name, tags)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM probe_systems WHERE device_id = $1`, id)
	return err
}

// Events lists a system's newest events; category "" means all.
func (s *Store) Events(ctx context.Context, id, category string, limit int) ([]Event, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ts, kind, category, level, ip, agent_version, actor, detail FROM probe_events
		WHERE device_id = $1 AND ($2 = '' OR category = $2)
		ORDER BY ts DESC, id DESC LIMIT $3`, id, category, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.TS, &e.Kind, &e.Category, &e.Level, &e.IP, &e.Version, &e.Actor, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func i64(p *uint64) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

// Series buckets samples with date_bin so each range returns a few hundred points.
// interval estimates traffic for rows recorded before byte deltas were stored.
func (s *Store) Series(ctx context.Context, id string, src Source, from time.Time, step, interval time.Duration) (*Series, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT date_bin(make_interval(secs => $3::float8), ts, TIMESTAMPTZ 'epoch') AS b,
			avg(cpu_pct)::float8, max(%[2]s)::float8,
			avg(cpu_user)::float8, avg(cpu_system)::float8, avg(cpu_iowait)::float8,
			avg(mem_used)::float8, max(mem_total)::float8, avg(swap_used)::float8,
			avg(disk_used)::float8, max(disk_total)::float8,
			avg(disk_read_bps)::float8, avg(disk_write_bps)::float8,
			avg(net_rx_bps)::float8, avg(net_tx_bps)::float8, max(%[3]s)::float8, max(%[4]s)::float8,
			sum(coalesce(net_rx_bytes, net_rx_bps * $4))::float8, sum(coalesce(net_tx_bytes, net_tx_bps * $4))::float8,
			avg(load1)::float8, avg(load5)::float8, avg(load15)::float8, max(temp_max)::float8, avg(procs)::float8,
			avg(net_rx_pps)::float8, avg(net_tx_pps)::float8, sum(net_errs)::float8, sum(net_drops)::float8,
			avg(tcp_estab)::float8, avg(tcp_time_wait)::float8, avg(tcp_listen)::float8,
			avg(retrans_pct)::float8, avg(latency_ms)::float8,
			sum(tcp_out_segs)::float8, sum(tcp_retrans_segs)::float8,
			avg(conn_tcp)::float8, avg(conn_udp)::float8, avg(conn_other)::float8
		FROM %[1]s
		WHERE device_id = $1 AND ts >= $2
		GROUP BY b ORDER BY b`, src.host, src.cpuMax, src.rxMax, src.txMax), id, from, step.Seconds(), interval.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := newSeries(step)
	for rows.Next() {
		var t time.Time
		var b bucket
		if err := rows.Scan(&t, &b.cpu, &b.cpuMax, &b.cpuUser, &b.cpuSystem, &b.cpuWait, &b.mem, &b.memT, &b.swap, &b.disk, &b.diskT, &b.dr, &b.dw,
			&b.rx, &b.tx, &b.rxMax, &b.txMax, &b.rxBytes, &b.txBytes, &b.l1, &b.l5, &b.l15, &b.temp, &b.procs,
			&b.rxPps, &b.txPps, &b.errs, &b.drops, &b.estab, &b.tw, &b.listen, &b.retrans, &b.latency, &b.outSegs, &b.retransSegs, &b.connTCP, &b.connUDP, &b.connOther); err != nil {
			return nil, err
		}
		out.add(t, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.diskParts(ctx, out, id, src, from, step); err != nil {
		return out, err
	}
	if err := s.gpuSeries(ctx, out, id, src, from, step); err != nil {
		return out, err
	}
	if err := s.tempSeries(ctx, out, id, src, from, step); err != nil {
		return out, err
	}
	return out, s.netNics(ctx, out, id, src, from, step)
}

// gpuSeries fills Series.GPUs with per-device utilization, aligned to out.T.
func (s *Store) gpuSeries(ctx context.Context, out *Series, id string, src Source, from time.Time, step time.Duration) error {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT date_bin(make_interval(secs => $3::float8), ts, TIMESTAMPTZ 'epoch') AS b, p.key, avg(p.value::float8)
		FROM %s, jsonb_each_text(gpus) p
		WHERE device_id = $1 AND ts >= $2 AND gpus IS NOT NULL
		GROUP BY b, p.key`, src.host), id, from, step.Seconds())
	if err != nil {
		return err
	}
	defer rows.Close()
	idx := make(map[int64]int, len(out.T))
	for i, ms := range out.T {
		idx[ms] = i
	}
	for rows.Next() {
		var t time.Time
		var name string
		var util float64
		if err := rows.Scan(&t, &name, &util); err != nil {
			return err
		}
		i, ok := idx[t.UnixMilli()]
		if !ok {
			continue
		}
		col := out.GPUs[name]
		if col == nil {
			col = make([]*float64, len(out.T))
			out.GPUs[name] = col
		}
		col[i] = &util
	}
	return rows.Err()
}

// tempSeries fills Series.TempBy with per-sensor Celsius, aligned to out.T.
func (s *Store) tempSeries(ctx context.Context, out *Series, id string, src Source, from time.Time, step time.Duration) error {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT date_bin(make_interval(secs => $3::float8), ts, TIMESTAMPTZ 'epoch') AS b, p.key, avg(p.value::float8)
		FROM %s, jsonb_each_text(temps) p
		WHERE device_id = $1 AND ts >= $2 AND temps IS NOT NULL
		GROUP BY b, p.key`, src.host), id, from, step.Seconds())
	if err != nil {
		return err
	}
	defer rows.Close()
	idx := make(map[int64]int, len(out.T))
	for i, ms := range out.T {
		idx[ms] = i
	}
	for rows.Next() {
		var t time.Time
		var name string
		var c float64
		if err := rows.Scan(&t, &name, &c); err != nil {
			return err
		}
		i, ok := idx[t.UnixMilli()]
		if !ok {
			continue
		}
		col := out.TempBy[name]
		if col == nil {
			col = make([]*float64, len(out.T))
			out.TempBy[name] = col
		}
		col[i] = &c
	}
	return rows.Err()
}

type nicPoint struct {
	Rx float64 `json:"rx"`
	Tx float64 `json:"tx"`
	Rb uint64  `json:"rb,omitempty"`
	Tb uint64  `json:"tb,omitempty"`
}

// diskParts fills Series.DiskParts with per-mount used bytes, aligned to out.T.
func (s *Store) diskParts(ctx context.Context, out *Series, id string, src Source, from time.Time, step time.Duration) error {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT date_bin(make_interval(secs => $3::float8), ts, TIMESTAMPTZ 'epoch') AS b, p.key, avg(p.value::float8)
		FROM %s, jsonb_each_text(disk_parts) p
		WHERE device_id = $1 AND ts >= $2 AND disk_parts IS NOT NULL
		GROUP BY b, p.key`, src.host), id, from, step.Seconds())
	if err != nil {
		return err
	}
	defer rows.Close()
	idx := make(map[int64]int, len(out.T))
	for i, ms := range out.T {
		idx[ms] = i
	}
	for rows.Next() {
		var t time.Time
		var mount string
		var used float64
		if err := rows.Scan(&t, &mount, &used); err != nil {
			return err
		}
		i, ok := idx[t.UnixMilli()]
		if !ok {
			continue
		}
		col := out.DiskParts[mount]
		if col == nil {
			col = make([]*float64, len(out.T))
			out.DiskParts[mount] = col
		}
		col[i] = &used
	}
	return rows.Err()
}

// netNics fills Series.NetNics with per-interface rates, aligned to out.T.
func (s *Store) netNics(ctx context.Context, out *Series, id string, src Source, from time.Time, step time.Duration) error {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT date_bin(make_interval(secs => $3::float8), ts, TIMESTAMPTZ 'epoch') AS b, p.key,
			avg((p.value->>'rx')::float8), avg((p.value->>'tx')::float8),
			sum(coalesce((p.value->>'rb')::float8, 0)), sum(coalesce((p.value->>'tb')::float8, 0))
		FROM %s, jsonb_each(net_nics) p
		WHERE device_id = $1 AND ts >= $2 AND net_nics IS NOT NULL
		GROUP BY b, p.key`, src.host), id, from, step.Seconds())
	if err != nil {
		return err
	}
	defer rows.Close()
	idx := make(map[int64]int, len(out.T))
	for i, ms := range out.T {
		idx[ms] = i
	}
	for rows.Next() {
		var t time.Time
		var name string
		var rx, tx, rb, tb float64
		if err := rows.Scan(&t, &name, &rx, &tx, &rb, &tb); err != nil {
			return err
		}
		i, ok := idx[t.UnixMilli()]
		if !ok {
			continue
		}
		line, ok := out.NetNics[name]
		if !ok {
			line = NicLine{Rx: make([]*float64, len(out.T)), Tx: make([]*float64, len(out.T)), RxBytes: make([]*float64, len(out.T)), TxBytes: make([]*float64, len(out.T))}
			out.NetNics[name] = line
		}
		line.Rx[i], line.Tx[i], line.RxBytes[i], line.TxBytes[i] = &rx, &tx, &rb, &tb
	}
	return rows.Err()
}

func (s *Store) Prune(ctx context.Context, metricsBefore, eventsBefore time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM probe_metrics WHERE ts < $1`, metricsBefore)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM probe_container_metrics WHERE ts < $1`, metricsBefore); err != nil {
		return 0, err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM probe_events WHERE ts < $1`, eventsBefore)
	return tag.RowsAffected(), err
}

var errNotFound = errors.New("system not found")
