package monitor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Source is the table set a history query reads: raw samples or a rollup tier.
type Source struct {
	Name       string
	Step       time.Duration // bucket width; 0 for raw samples
	host       string
	containers string
	// Columns holding per-bucket peaks (raw samples are their own peak).
	cpuMax, rxMax, txMax string
}

var (
	SourceRaw = Source{Name: "raw", host: "probe_metrics", containers: "probe_container_metrics",
		cpuMax: "cpu_pct", rxMax: "net_rx_bps", txMax: "net_tx_bps"}
	Source5m = Source{Name: "5m", Step: 5 * time.Minute, host: "probe_metrics_5m", containers: "probe_container_metrics_5m",
		cpuMax: "cpu_max", rxMax: "net_rx_max", txMax: "net_tx_max"}
	Source1h = Source{Name: "1h", Step: time.Hour, host: "probe_metrics_1h", containers: "probe_container_metrics_1h",
		cpuMax: "cpu_max", rxMax: "net_rx_max", txMax: "net_tx_max"}
)

// PickSource serves wide steps from rollups, and anything older than the raw
// retention from the 5-minute tier. Rollup steps are whole multiples of the
// tier so each bucket holds the same number of rows.
func PickSource(from time.Time, step, rawRetention time.Duration) (Source, time.Duration) {
	src := SourceRaw
	switch {
	case step >= time.Hour:
		src = Source1h
	case step >= 15*time.Minute || time.Since(from) > rawRetention:
		src = Source5m
	default:
		return src, step
	}
	return src, (step + src.Step - 1) / src.Step * src.Step
}

// Rollup columns (NOT NULL ones are scanned into plain floats by Series).
const rollupHostCols = `
	cpu_pct double precision NOT NULL, cpu_max double precision NOT NULL,
	mem_used double precision NOT NULL, mem_total double precision NOT NULL,
	swap_used double precision NOT NULL, swap_total double precision NOT NULL,
	disk_used double precision NOT NULL, disk_total double precision NOT NULL,
	disk_read_bps double precision NOT NULL, disk_write_bps double precision NOT NULL,
	net_rx_bps double precision NOT NULL, net_tx_bps double precision NOT NULL,
	net_rx_max double precision NOT NULL, net_tx_max double precision NOT NULL,
	net_rx_bytes double precision NOT NULL, net_tx_bytes double precision NOT NULL,
	load1 double precision NOT NULL, load5 double precision, load15 double precision,
	temp_max double precision, procs double precision NOT NULL,
	net_rx_pps double precision, net_tx_pps double precision, net_errs double precision, net_drops double precision,
	tcp_estab double precision, tcp_time_wait double precision, tcp_listen double precision,
	retrans_pct double precision, latency_ms double precision,
	tcp_out_segs double precision, tcp_retrans_segs double precision,
	conn_tcp double precision, conn_udp double precision, conn_other double precision,
	disk_parts jsonb, net_nics jsonb`

const rollupCtrCols = `
	cpu_pct double precision, mem_used double precision NOT NULL, mem_limit double precision NOT NULL,
	net_rx_bps double precision, net_tx_bps double precision, blk_read_bps double precision, blk_write_bps double precision,
	pids double precision`

var rollupSchema = func() string {
	var b strings.Builder
	for _, t := range []Source{Source5m, Source1h} {
		fmt.Fprintf(&b, `
CREATE TABLE IF NOT EXISTS %s (
	device_id text NOT NULL REFERENCES probe_systems(device_id) ON DELETE CASCADE ON UPDATE CASCADE,
	ts timestamptz NOT NULL, %s,
	PRIMARY KEY (device_id, ts)
);
CREATE TABLE IF NOT EXISTS %s (
	device_id text NOT NULL REFERENCES probe_systems(device_id) ON DELETE CASCADE ON UPDATE CASCADE,
	ts timestamptz NOT NULL, name text NOT NULL, %s,
	PRIMARY KEY (device_id, ts, name)
);`, t.host, rollupHostCols, t.containers, rollupCtrCols)
	}
	return b.String()
}()

// agg folds one column into a bucket. raw overrides the expression used when
// reading raw samples ("" means fn(col)).
type agg struct{ col, fn, raw string }

var hostAggs = []agg{
	{"cpu_pct", "avg", ""}, {"cpu_max", "max", "max(cpu_pct)"},
	{"mem_used", "avg", ""}, {"mem_total", "max", ""}, {"swap_used", "avg", ""}, {"swap_total", "max", ""},
	{"disk_used", "avg", ""}, {"disk_total", "max", ""}, {"disk_read_bps", "avg", ""}, {"disk_write_bps", "avg", ""},
	{"net_rx_bps", "avg", ""}, {"net_tx_bps", "avg", ""},
	{"net_rx_max", "max", "max(net_rx_bps)"}, {"net_tx_max", "max", "max(net_tx_bps)"},
	{"net_rx_bytes", "sum", "sum(coalesce(net_rx_bytes, net_rx_bps * {iv}))"},
	{"net_tx_bytes", "sum", "sum(coalesce(net_tx_bytes, net_tx_bps * {iv}))"},
	{"load1", "avg", ""}, {"load5", "avg", ""}, {"load15", "avg", ""}, {"temp_max", "max", ""}, {"procs", "avg", ""},
	{"net_rx_pps", "avg", ""}, {"net_tx_pps", "avg", ""}, {"net_errs", "sum", ""}, {"net_drops", "sum", ""},
	{"tcp_estab", "avg", ""}, {"tcp_time_wait", "avg", ""}, {"tcp_listen", "avg", ""},
	{"retrans_pct", "avg", ""}, {"latency_ms", "avg", ""}, {"tcp_out_segs", "sum", ""}, {"tcp_retrans_segs", "sum", ""},
	{"conn_tcp", "avg", ""}, {"conn_udp", "avg", ""}, {"conn_other", "avg", ""},
}

var ctrAggs = []agg{
	{"cpu_pct", "avg", ""}, {"mem_used", "avg", ""}, {"mem_limit", "max", ""},
	{"net_rx_bps", "avg", ""}, {"net_tx_bps", "avg", ""}, {"blk_read_bps", "avg", ""}, {"blk_write_bps", "avg", ""},
	{"pids", "avg", ""},
}

func rollupSQL(from, to Source, interval time.Duration) (host, ctr string) {
	bin := fmt.Sprintf("date_bin(INTERVAL '%d seconds', ts, TIMESTAMPTZ 'epoch')", int(to.Step/time.Second))
	iv := strconv.FormatFloat(interval.Seconds(), 'f', -1, 64)
	build := func(aggs []agg) (cols, exprs, sets string) {
		var c, e, s []string
		for _, a := range aggs {
			x := a.fn + "(" + a.col + ")"
			if from.Step == 0 && a.raw != "" {
				x = strings.ReplaceAll(a.raw, "{iv}", iv)
			}
			c, e, s = append(c, a.col), append(e, x), append(s, a.col+" = EXCLUDED."+a.col)
		}
		return strings.Join(c, ", "), strings.Join(e, ", "), strings.Join(s, ", ")
	}
	cols, exprs, sets := build(hostAggs)
	host = fmt.Sprintf(`
		INSERT INTO %[1]s (device_id, ts, %[3]s, disk_parts, net_nics)
		SELECT a.*, p.parts, n.nics FROM (
			SELECT device_id, %[5]s AS b, %[4]s FROM %[2]s WHERE ts >= $1 GROUP BY 1, 2) a
		LEFT JOIN (
			SELECT device_id, b, jsonb_object_agg(key, v) AS parts FROM (
				SELECT device_id, %[5]s AS b, p.key, round(avg(p.value::float8)) AS v
				FROM %[2]s, jsonb_each_text(disk_parts) p WHERE ts >= $1 AND disk_parts IS NOT NULL GROUP BY 1, 2, 3) x
			GROUP BY 1, 2) p USING (device_id, b)
		LEFT JOIN (
			SELECT device_id, b, jsonb_object_agg(key, jsonb_build_object('rx', rx, 'tx', tx, 'rb', rb, 'tb', tb)) AS nics FROM (
				SELECT device_id, %[5]s AS b, p.key,
					avg((p.value->>'rx')::float8) AS rx, avg((p.value->>'tx')::float8) AS tx,
					sum(coalesce((p.value->>'rb')::float8, 0)) AS rb, sum(coalesce((p.value->>'tb')::float8, 0)) AS tb
				FROM %[2]s, jsonb_each(net_nics) p WHERE ts >= $1 AND net_nics IS NOT NULL GROUP BY 1, 2, 3) x
			GROUP BY 1, 2) n USING (device_id, b)
		ON CONFLICT (device_id, ts) DO UPDATE SET %[6]s, disk_parts = EXCLUDED.disk_parts, net_nics = EXCLUDED.net_nics`,
		to.host, from.host, cols, exprs, bin, sets)
	cols, exprs, sets = build(ctrAggs)
	ctr = fmt.Sprintf(`
		INSERT INTO %[1]s (device_id, ts, name, %[3]s)
		SELECT device_id, %[5]s, name, %[4]s FROM %[2]s WHERE ts >= $1 GROUP BY 1, 2, 3
		ON CONFLICT (device_id, ts, name) DO UPDATE SET %[6]s`,
		to.containers, from.containers, cols, exprs, bin, sets)
	return host, ctr
}

// Rollup (re)builds the 5-minute tier from raw samples and the hourly tier
// from the 5-minute one, for buckets starting at or after since. Re-running
// over the same window is idempotent; the newest bucket fills in as samples arrive.
func (s *Store) Rollup(ctx context.Context, since time.Time, interval time.Duration) error {
	for _, step := range [][2]Source{{SourceRaw, Source5m}, {Source5m, Source1h}} {
		host, ctr := rollupSQL(step[0], step[1], interval)
		from := since.Truncate(step[1].Step)
		if _, err := s.db.Exec(ctx, host, from); err != nil {
			return fmt.Errorf("rollup %s: %w", step[1].host, err)
		}
		if _, err := s.db.Exec(ctx, ctr, from); err != nil {
			return fmt.Errorf("rollup %s: %w", step[1].containers, err)
		}
	}
	return nil
}

// LastRollup is the newest 5-minute bucket (zero when the tier is empty).
func (s *Store) LastRollup(ctx context.Context) (time.Time, error) {
	var t *time.Time
	err := s.db.QueryRow(ctx, `SELECT max(ts) FROM `+Source5m.host).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, err
	}
	return *t, nil
}

// PruneRollups drops rollup buckets older than each tier's cutoff.
func (s *Store) PruneRollups(ctx context.Context, before5m, before1h time.Time) error {
	for _, p := range []struct {
		src    Source
		before time.Time
	}{{Source5m, before5m}, {Source1h, before1h}} {
		for _, table := range []string{p.src.host, p.src.containers} {
			if _, err := s.db.Exec(ctx, `DELETE FROM `+table+` WHERE ts < $1`, p.before); err != nil {
				return err
			}
		}
	}
	return nil
}
