package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"govmomi-wmks/internal/guest"
)

const schema = `
CREATE TABLE IF NOT EXISTS probe_systems (
	bios_uuid     text PRIMARY KEY,
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
	connected_at  timestamptz
);
CREATE TABLE IF NOT EXISTS probe_metrics (
	bios_uuid      text NOT NULL REFERENCES probe_systems(bios_uuid) ON DELETE CASCADE,
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
	temp_max       real,
	procs          integer NOT NULL DEFAULT 0,
	PRIMARY KEY (bios_uuid, ts)
);
CREATE TABLE IF NOT EXISTS probe_events (
	id        bigserial PRIMARY KEY,
	bios_uuid text NOT NULL REFERENCES probe_systems(bios_uuid) ON DELETE CASCADE,
	ts        timestamptz NOT NULL DEFAULT now(),
	kind      text NOT NULL,
	detail    text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS probe_events_bios_ts ON probe_events (bios_uuid, ts DESC);
ALTER TABLE probe_metrics ADD COLUMN IF NOT EXISTS disk_parts jsonb;
`

// Store persists systems, samples and online/offline events in PostgreSQL.
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
	if _, err := db.Exec(ctx, `UPDATE probe_systems SET online = false WHERE online`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() { s.db.Close() }

func (s *Store) LoadSystems(ctx context.Context) ([]*System, error) {
	rows, err := s.db.Query(ctx, `
		SELECT bios_uuid, name, hostname, agent_version, remote_addr, tags, info, latest,
		       first_seen, last_seen, connected_at
		FROM probe_systems`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*System
	for rows.Next() {
		var sys System
		var info, latest []byte
		var connected *time.Time
		if err := rows.Scan(&sys.BiosUUID, &sys.Name, &sys.HostName, &sys.Version, &sys.RemoteAddr,
			&sys.Tags, &info, &latest, &sys.FirstSeen, &sys.LastSeen, &connected); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(info, &sys.Info)
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
	_, err := s.db.Exec(ctx, `
		INSERT INTO probe_systems (bios_uuid, name, hostname, agent_version, remote_addr, online, first_seen, last_seen, connected_at)
		VALUES ($1, $2, $3, $4, $5, true, $6, $6, $6)
		ON CONFLICT (bios_uuid) DO UPDATE SET
			hostname = EXCLUDED.hostname, agent_version = EXCLUDED.agent_version,
			remote_addr = EXCLUDED.remote_addr, online = true,
			last_seen = EXCLUDED.last_seen, connected_at = EXCLUDED.connected_at`,
		sys.BiosUUID, sys.Name, sys.HostName, sys.Version, sys.RemoteAddr, sys.ConnectedAt)
	if err != nil {
		return err
	}
	return s.event(ctx, sys.BiosUUID, "online", "from "+sys.RemoteAddr+" agent "+sys.Version, sys.ConnectedAt)
}

func (s *Store) Disconnect(ctx context.Context, bios string, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE probe_systems SET online = false, last_seen = $2 WHERE bios_uuid = $1`, bios, at); err != nil {
		return err
	}
	return s.event(ctx, bios, "offline", "", at)
}

func (s *Store) event(ctx context.Context, bios, kind, detail string, at time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO probe_events (bios_uuid, ts, kind, detail) VALUES ($1, $2, $3, $4)`, bios, at, kind, detail)
	return err
}

func (s *Store) Record(ctx context.Context, bios string, info guest.SysInfo, smp Sample) error {
	infoJSON, _ := json.Marshal(info)
	latestJSON, _ := json.Marshal(smp)
	b := &pgx.Batch{}
	b.Queue(`UPDATE probe_systems SET info = $2, latest = $3, last_seen = $4, hostname = COALESCE(NULLIF($5, ''), hostname) WHERE bios_uuid = $1`,
		bios, infoJSON, latestJSON, smp.TS, info.HostName)
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
		parts, _ = json.Marshal(used)
	}
	b.Queue(`INSERT INTO probe_metrics (bios_uuid, ts, cpu_pct, mem_used, mem_total, swap_used, swap_total,
			disk_used, disk_total, disk_read_bps, disk_write_bps, net_rx_bps, net_tx_bps, load1, temp_max, procs, disk_parts)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT DO NOTHING`,
		bios, smp.TS, smp.CPUPct, int64(smp.MemUsed), int64(smp.MemTotal), int64(smp.SwapUsed), int64(smp.SwapTotal),
		int64(smp.DiskUsed), int64(smp.DiskTotal), smp.DiskReadBps, smp.DiskWriteBps, smp.NetRxBps, smp.NetTxBps,
		smp.Load[0], temp, smp.Procs, parts)
	return s.db.SendBatch(ctx, b).Close()
}

func (s *Store) UpdateMeta(ctx context.Context, bios string, name *string, tags []string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE probe_systems SET
			name = COALESCE($2, name),
			tags = COALESCE($3, tags)
		WHERE bios_uuid = $1`, bios, name, tags)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, bios string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM probe_systems WHERE bios_uuid = $1`, bios)
	return err
}

func (s *Store) Events(ctx context.Context, bios string, limit int) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT ts, kind, detail FROM probe_events WHERE bios_uuid = $1 ORDER BY ts DESC LIMIT $2`, bios, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.TS, &e.Kind, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Series buckets samples with date_bin so each range returns a few hundred points.
func (s *Store) Series(ctx context.Context, bios string, from time.Time, step time.Duration) (*Series, error) {
	rows, err := s.db.Query(ctx, `
		SELECT date_bin(make_interval(secs => $3::float8), ts, TIMESTAMPTZ 'epoch') AS b,
			avg(cpu_pct), max(cpu_pct),
			avg(mem_used)::float8, max(mem_total)::float8, avg(swap_used)::float8,
			avg(disk_used)::float8, max(disk_total)::float8,
			avg(disk_read_bps), avg(disk_write_bps),
			avg(net_rx_bps), avg(net_tx_bps),
			avg(load1), max(temp_max), avg(procs)::float8
		FROM probe_metrics
		WHERE bios_uuid = $1 AND ts >= $2
		GROUP BY b ORDER BY b`, bios, from, step.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := newSeries(step)
	for rows.Next() {
		var t time.Time
		var cpu, cpuMax, mem, memT, swap, disk, diskT, dr, dw, rx, tx, l1, procs float64
		var temp *float64
		if err := rows.Scan(&t, &cpu, &cpuMax, &mem, &memT, &swap, &disk, &diskT, &dr, &dw, &rx, &tx, &l1, &temp, &procs); err != nil {
			return nil, err
		}
		out.add(t, cpu, cpuMax, mem, memT, swap, disk, diskT, dr, dw, rx, tx, l1, temp, procs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.diskParts(ctx, out, bios, from, step)
}

// diskParts fills Series.DiskParts with per-mount used bytes, aligned to out.T.
func (s *Store) diskParts(ctx context.Context, out *Series, bios string, from time.Time, step time.Duration) error {
	rows, err := s.db.Query(ctx, `
		SELECT date_bin(make_interval(secs => $3::float8), ts, TIMESTAMPTZ 'epoch') AS b, p.key, avg(p.value::float8)
		FROM probe_metrics, jsonb_each_text(disk_parts) p
		WHERE bios_uuid = $1 AND ts >= $2 AND disk_parts IS NOT NULL
		GROUP BY b, p.key`, bios, from, step.Seconds())
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

func (s *Store) Prune(ctx context.Context, metricsBefore, eventsBefore time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM probe_metrics WHERE ts < $1`, metricsBefore)
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM probe_events WHERE ts < $1`, eventsBefore)
	return tag.RowsAffected(), err
}

var errNotFound = errors.New("system not found")
