package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HostMetric is one raw host metrics sample.
type HostMetric struct {
	TS        time.Time
	CPUPct    float64
	Load1     float64
	MemTotal  int64
	MemAvail  int64
	DiskTotal int64
	DiskUsed  int64
	NetRxBps  float64
	NetTxBps  float64
}

// ContainerMetric is one raw container metrics sample.
type ContainerMetric struct {
	ContainerID string
	TS          time.Time
	CPUPct      float64
	MemUsage    int64
	MemLimit    int64
	NetRxBps    float64
	NetTxBps    float64
	BlkReadBps  float64
	BlkWriteBps float64
	Pids        int
}

// InsertHostMetrics writes raw host samples with a batched INSERT ... ON CONFLICT DO NOTHING.
func (s *Store) InsertHostMetrics(ctx context.Context, orgID, hostID uuid.UUID, ms []HostMetric) error {
	if len(ms) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	const q = `
		INSERT INTO host_metrics (
			org_id, host_id, ts, cpu_pct, load1, mem_total, mem_avail,
			disk_total, disk_used, net_rx_bps, net_tx_bps
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (host_id, ts) DO NOTHING`
	for _, m := range ms {
		batch.Queue(q, orgID, hostID, m.TS, m.CPUPct, m.Load1, m.MemTotal, m.MemAvail,
			m.DiskTotal, m.DiskUsed, m.NetRxBps, m.NetTxBps)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }() // the Exec errors below are the ones that matter
	for range ms {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: InsertHostMetrics: %w", err)
		}
	}
	return nil
}

// InsertContainerMetrics writes raw container samples with a batched INSERT ... ON CONFLICT DO NOTHING.
func (s *Store) InsertContainerMetrics(ctx context.Context, orgID, hostID uuid.UUID, ms []ContainerMetric) error {
	if len(ms) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	const q = `
		INSERT INTO container_metrics (
			org_id, host_id, container_id, ts, cpu_pct, mem_usage, mem_limit,
			net_rx_bps, net_tx_bps, blk_read_bps, blk_write_bps, pids
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (host_id, container_id, ts) DO NOTHING`
	for _, m := range ms {
		batch.Queue(q, orgID, hostID, m.ContainerID, m.TS, m.CPUPct, m.MemUsage, m.MemLimit,
			m.NetRxBps, m.NetTxBps, m.BlkReadBps, m.BlkWriteBps, m.Pids)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }() // the Exec errors below are the ones that matter
	for range ms {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: InsertContainerMetrics: %w", err)
		}
	}
	return nil
}

// Resolution of a query result.
type Resolution string

const (
	ResolutionRaw Resolution = "raw"
	Resolution1m  Resolution = "1m"
	Resolution1h  Resolution = "1h"
)

// Retention of each resolution (ADR 0001).
const (
	RawRetention      = 48 * time.Hour
	Rollup1mRetention = 30 * 24 * time.Hour
	Rollup1hRetention = 395 * 24 * time.Hour
)

// ResolutionFor picks raw for spans <= 6h, 1m for <= 7 days, 1h otherwise,
// falling back to a coarser table when the window starts before the finer
// one's retention.
func ResolutionFor(now, from, to time.Time) Resolution {
	span := to.Sub(from)
	if span < 0 {
		span = -span
		from = to
	}
	// Also respect retention: raw rows exist for 48 h, 1 min rollups for
	// 30 days, so an old window must read the coarser table even if short.
	switch {
	case span <= 6*time.Hour && from.After(now.Add(-RawRetention)):
		return ResolutionRaw
	case span <= 7*24*time.Hour && from.After(now.Add(-Rollup1mRetention)):
		return Resolution1m
	default:
		return Resolution1h
	}
}

// Point is one value of a series; for rollups Value is the average and Max the maximum (for raw, Max == Value).
type Point struct {
	TS    time.Time
	Value float64
	Max   float64
}

type seriesCols struct {
	rawValue string
	rawMax   string
	avg      string
	max      string
}

// Fixed allowlists: column expressions never come from user input concatenation.
var hostSeriesMetrics = map[string]seriesCols{
	"cpu_pct":    {rawValue: "cpu_pct", rawMax: "cpu_pct", avg: "cpu_pct_avg", max: "cpu_pct_max"},
	"load1":      {rawValue: "load1", rawMax: "load1", avg: "load1_avg", max: "load1_max"},
	"mem_used":   {rawValue: "(mem_total - mem_avail)", rawMax: "(mem_total - mem_avail)", avg: "(mem_total_max - mem_avail_avg)", max: "(mem_total_max - mem_avail_min)"},
	"mem_total":  {rawValue: "mem_total", rawMax: "mem_total", avg: "mem_total_max", max: "mem_total_max"},
	"disk_used":  {rawValue: "disk_used", rawMax: "disk_used", avg: "disk_used_avg", max: "disk_used_max"},
	"disk_total": {rawValue: "disk_total", rawMax: "disk_total", avg: "disk_total_max", max: "disk_total_max"},
	"net_rx_bps": {rawValue: "net_rx_bps", rawMax: "net_rx_bps", avg: "net_rx_bps_avg", max: "net_rx_bps_max"},
	"net_tx_bps": {rawValue: "net_tx_bps", rawMax: "net_tx_bps", avg: "net_tx_bps_avg", max: "net_tx_bps_max"},
}

var containerSeriesMetrics = map[string]seriesCols{
	"cpu_pct":       {rawValue: "cpu_pct", rawMax: "cpu_pct", avg: "cpu_pct_avg", max: "cpu_pct_max"},
	"mem_usage":     {rawValue: "mem_usage", rawMax: "mem_usage", avg: "mem_usage_avg", max: "mem_usage_max"},
	"mem_limit":     {rawValue: "mem_limit", rawMax: "mem_limit", avg: "mem_limit_max", max: "mem_limit_max"},
	"net_rx_bps":    {rawValue: "net_rx_bps", rawMax: "net_rx_bps", avg: "net_rx_bps_avg", max: "net_rx_bps_max"},
	"net_tx_bps":    {rawValue: "net_tx_bps", rawMax: "net_tx_bps", avg: "net_tx_bps_avg", max: "net_tx_bps_max"},
	"blk_read_bps":  {rawValue: "blk_read_bps", rawMax: "blk_read_bps", avg: "blk_read_bps_avg", max: "blk_read_bps_max"},
	"blk_write_bps": {rawValue: "blk_write_bps", rawMax: "blk_write_bps", avg: "blk_write_bps_avg", max: "blk_write_bps_max"},
	"pids":          {rawValue: "pids", rawMax: "pids", avg: "pids_avg", max: "pids_max"},
}

// HostSeries returns one metric for a host between from and to at ResolutionFor(now, from, to).
// metric is one of "cpu_pct","load1","mem_used","mem_total","disk_used","disk_total","net_rx_bps","net_tx_bps".
// Unknown metric -> error. Column names come from a fixed allowlist map.
func (s *Store) HostSeries(ctx context.Context, orgID, hostID uuid.UUID, metric string, from, to time.Time) (Resolution, []Point, error) {
	cols, ok := hostSeriesMetrics[metric]
	if !ok {
		return "", nil, fmt.Errorf("store: HostSeries: unknown metric %q", metric)
	}
	res := ResolutionFor(time.Now(), from, to)
	var (
		q    string
		args []any
	)
	switch res {
	case ResolutionRaw:
		q = fmt.Sprintf(`
			SELECT ts, %s::float8, %s::float8
			FROM host_metrics
			WHERE org_id = $1 AND host_id = $2 AND ts >= $3 AND ts <= $4
			ORDER BY ts`, cols.rawValue, cols.rawMax)
		args = []any{orgID, hostID, from, to}
	case Resolution1m:
		q = fmt.Sprintf(`
			SELECT ts, %s::float8, %s::float8
			FROM host_metrics_1m
			WHERE org_id = $1 AND host_id = $2 AND ts >= $3 AND ts <= $4
			ORDER BY ts`, cols.avg, cols.max)
		args = []any{orgID, hostID, from, to}
	default:
		q = fmt.Sprintf(`
			SELECT ts, %s::float8, %s::float8
			FROM host_metrics_1h
			WHERE org_id = $1 AND host_id = $2 AND ts >= $3 AND ts <= $4
			ORDER BY ts`, cols.avg, cols.max)
		args = []any{orgID, hostID, from, to}
	}
	pts, err := s.queryPoints(ctx, q, args...)
	if err != nil {
		return "", nil, fmt.Errorf("store: HostSeries: %w", err)
	}
	return res, pts, nil
}

// ContainerSeries is the same for one container:
// "cpu_pct","mem_usage","mem_limit","net_rx_bps","net_tx_bps","blk_read_bps","blk_write_bps","pids".
func (s *Store) ContainerSeries(ctx context.Context, orgID, hostID uuid.UUID, containerID, metric string, from, to time.Time) (Resolution, []Point, error) {
	cols, ok := containerSeriesMetrics[metric]
	if !ok {
		return "", nil, fmt.Errorf("store: ContainerSeries: unknown metric %q", metric)
	}
	res := ResolutionFor(time.Now(), from, to)
	var (
		q    string
		args []any
	)
	switch res {
	case ResolutionRaw:
		q = fmt.Sprintf(`
			SELECT ts, %s::float8, %s::float8
			FROM container_metrics
			WHERE org_id = $1 AND host_id = $2 AND container_id = $3 AND ts >= $4 AND ts <= $5
			ORDER BY ts`, cols.rawValue, cols.rawMax)
		args = []any{orgID, hostID, containerID, from, to}
	case Resolution1m:
		q = fmt.Sprintf(`
			SELECT ts, %s::float8, %s::float8
			FROM container_metrics_1m
			WHERE org_id = $1 AND host_id = $2 AND container_id = $3 AND ts >= $4 AND ts <= $5
			ORDER BY ts`, cols.avg, cols.max)
		args = []any{orgID, hostID, containerID, from, to}
	default:
		q = fmt.Sprintf(`
			SELECT ts, %s::float8, %s::float8
			FROM container_metrics_1h
			WHERE org_id = $1 AND host_id = $2 AND container_id = $3 AND ts >= $4 AND ts <= $5
			ORDER BY ts`, cols.avg, cols.max)
		args = []any{orgID, hostID, containerID, from, to}
	}
	pts, err := s.queryPoints(ctx, q, args...)
	if err != nil {
		return "", nil, fmt.Errorf("store: ContainerSeries: %w", err)
	}
	return res, pts, nil
}

func (s *Store) queryPoints(ctx context.Context, q string, args ...any) ([]Point, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.TS, &p.Value, &p.Max); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EnsurePartitions creates daily partitions of host_metrics and container_metrics covering
// [day(now)-1, day(now)+2] (UTC days, names like host_metrics_p20261024). Idempotent.
func (s *Store) EnsurePartitions(ctx context.Context, now time.Time) error {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for i := -1; i <= 2; i++ {
		start := day.AddDate(0, 0, i)
		end := start.AddDate(0, 0, 1)
		suffix := start.Format("20060102")
		for _, parent := range []string{"host_metrics", "container_metrics"} {
			name := parent + "_p" + suffix
			q := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)`,
				pgQuoteIdent(name), pgQuoteIdent(parent),
				pgQuoteLiteral(start.Format(time.RFC3339)),
				pgQuoteLiteral(end.Format(time.RFC3339)),
			)
			if _, err := s.pool.Exec(ctx, q); err != nil {
				return fmt.Errorf("store: EnsurePartitions: %w", err)
			}
		}
	}
	return nil
}

// DropExpiredPartitions drops raw partitions whose upper bound is older than now-rawRetention.
// Returns the number dropped.
func (s *Store) DropExpiredPartitions(ctx context.Context, now time.Time, rawRetention time.Duration) (int, error) {
	cutoff := now.UTC().Add(-rawRetention)
	dropped := 0
	for _, parent := range []string{"host_metrics", "container_metrics"} {
		names, err := s.listMetricPartitions(ctx, parent)
		if err != nil {
			return dropped, fmt.Errorf("store: DropExpiredPartitions: %w", err)
		}
		for _, name := range names {
			upper, ok := partitionUpperBound(name, parent)
			if !ok || !upper.Before(cutoff) {
				continue
			}
			if _, err := s.pool.Exec(ctx, `DROP TABLE IF EXISTS `+pgQuoteIdent(name)); err != nil {
				return dropped, fmt.Errorf("store: DropExpiredPartitions: %w", err)
			}
			dropped++
		}
	}
	return dropped, nil
}

func (s *Store) listMetricPartitions(ctx context.Context, parent string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = $1`, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func partitionUpperBound(name, parent string) (time.Time, bool) {
	prefix := parent + "_p"
	if !strings.HasPrefix(name, prefix) {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("20060102", strings.TrimPrefix(name, prefix), time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return day.AddDate(0, 0, 1), true
}

// Rollup aggregates complete buckets: raw -> 1m for minutes ending at or before now truncated to the minute and
// starting after now-2h, and 1m -> 1h for hours ending at or before now truncated to the hour and starting after now-48h.
func (s *Store) Rollup(ctx context.Context, now time.Time) error {
	now = now.UTC()
	if err := s.rollupHost1m(ctx, now); err != nil {
		return err
	}
	if err := s.rollupContainer1m(ctx, now); err != nil {
		return err
	}
	if err := s.rollupHost1h(ctx, now); err != nil {
		return err
	}
	if err := s.rollupContainer1h(ctx, now); err != nil {
		return err
	}
	return nil
}

func (s *Store) rollupHost1m(ctx context.Context, now time.Time) error {
	to := now.Truncate(time.Minute)
	from := now.Add(-2 * time.Hour)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO host_metrics_1m (
			org_id, host_id, ts,
			cpu_pct_avg, cpu_pct_max,
			load1_avg, load1_max,
			mem_total_max, mem_avail_avg, mem_avail_min,
			disk_total_max, disk_used_avg, disk_used_max,
			net_rx_bps_avg, net_rx_bps_max,
			net_tx_bps_avg, net_tx_bps_max,
			samples
		)
		SELECT
			org_id, host_id,
			date_bin('1 minute', ts, TIMESTAMPTZ 'epoch') AS bucket,
			AVG(cpu_pct)::real, MAX(cpu_pct)::real,
			AVG(load1)::real, MAX(load1)::real,
			MAX(mem_total), AVG(mem_avail)::real, MIN(mem_avail)::real,
			MAX(disk_total), AVG(disk_used)::real, MAX(disk_used)::real,
			AVG(net_rx_bps)::real, MAX(net_rx_bps)::real,
			AVG(net_tx_bps)::real, MAX(net_tx_bps)::real,
			COUNT(*)::int
		FROM host_metrics
		WHERE ts > $1 AND ts <= $2
		GROUP BY org_id, host_id, bucket
		HAVING date_bin('1 minute', MIN(ts), TIMESTAMPTZ 'epoch') > $1
		   AND date_bin('1 minute', MIN(ts), TIMESTAMPTZ 'epoch') + interval '1 minute' <= $2
		ON CONFLICT (host_id, ts) DO UPDATE SET
			org_id = EXCLUDED.org_id,
			cpu_pct_avg = EXCLUDED.cpu_pct_avg,
			cpu_pct_max = EXCLUDED.cpu_pct_max,
			load1_avg = EXCLUDED.load1_avg,
			load1_max = EXCLUDED.load1_max,
			mem_total_max = EXCLUDED.mem_total_max,
			mem_avail_avg = EXCLUDED.mem_avail_avg,
			mem_avail_min = EXCLUDED.mem_avail_min,
			disk_total_max = EXCLUDED.disk_total_max,
			disk_used_avg = EXCLUDED.disk_used_avg,
			disk_used_max = EXCLUDED.disk_used_max,
			net_rx_bps_avg = EXCLUDED.net_rx_bps_avg,
			net_rx_bps_max = EXCLUDED.net_rx_bps_max,
			net_tx_bps_avg = EXCLUDED.net_tx_bps_avg,
			net_tx_bps_max = EXCLUDED.net_tx_bps_max,
			samples = EXCLUDED.samples`,
		from, to,
	)
	if err != nil {
		return fmt.Errorf("store: Rollup: %w", err)
	}
	return nil
}

func (s *Store) rollupContainer1m(ctx context.Context, now time.Time) error {
	to := now.Truncate(time.Minute)
	from := now.Add(-2 * time.Hour)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO container_metrics_1m (
			org_id, host_id, container_id, ts,
			cpu_pct_avg, cpu_pct_max,
			mem_usage_avg, mem_usage_max, mem_limit_max,
			net_rx_bps_avg, net_rx_bps_max,
			net_tx_bps_avg, net_tx_bps_max,
			blk_read_bps_avg, blk_read_bps_max,
			blk_write_bps_avg, blk_write_bps_max,
			pids_avg, pids_max,
			samples
		)
		SELECT
			org_id, host_id, container_id,
			date_bin('1 minute', ts, TIMESTAMPTZ 'epoch') AS bucket,
			AVG(cpu_pct)::real, MAX(cpu_pct)::real,
			AVG(mem_usage)::real, MAX(mem_usage)::real, MAX(mem_limit),
			AVG(net_rx_bps)::real, MAX(net_rx_bps)::real,
			AVG(net_tx_bps)::real, MAX(net_tx_bps)::real,
			AVG(blk_read_bps)::real, MAX(blk_read_bps)::real,
			AVG(blk_write_bps)::real, MAX(blk_write_bps)::real,
			AVG(pids)::real, MAX(pids)::real,
			COUNT(*)::int
		FROM container_metrics
		WHERE ts > $1 AND ts <= $2
		GROUP BY org_id, host_id, container_id, bucket
		HAVING date_bin('1 minute', MIN(ts), TIMESTAMPTZ 'epoch') > $1
		   AND date_bin('1 minute', MIN(ts), TIMESTAMPTZ 'epoch') + interval '1 minute' <= $2
		ON CONFLICT (host_id, container_id, ts) DO UPDATE SET
			org_id = EXCLUDED.org_id,
			cpu_pct_avg = EXCLUDED.cpu_pct_avg,
			cpu_pct_max = EXCLUDED.cpu_pct_max,
			mem_usage_avg = EXCLUDED.mem_usage_avg,
			mem_usage_max = EXCLUDED.mem_usage_max,
			mem_limit_max = EXCLUDED.mem_limit_max,
			net_rx_bps_avg = EXCLUDED.net_rx_bps_avg,
			net_rx_bps_max = EXCLUDED.net_rx_bps_max,
			net_tx_bps_avg = EXCLUDED.net_tx_bps_avg,
			net_tx_bps_max = EXCLUDED.net_tx_bps_max,
			blk_read_bps_avg = EXCLUDED.blk_read_bps_avg,
			blk_read_bps_max = EXCLUDED.blk_read_bps_max,
			blk_write_bps_avg = EXCLUDED.blk_write_bps_avg,
			blk_write_bps_max = EXCLUDED.blk_write_bps_max,
			pids_avg = EXCLUDED.pids_avg,
			pids_max = EXCLUDED.pids_max,
			samples = EXCLUDED.samples`,
		from, to,
	)
	if err != nil {
		return fmt.Errorf("store: Rollup: %w", err)
	}
	return nil
}

func (s *Store) rollupHost1h(ctx context.Context, now time.Time) error {
	to := now.Truncate(time.Hour)
	from := now.Add(-48 * time.Hour)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO host_metrics_1h (
			org_id, host_id, ts,
			cpu_pct_avg, cpu_pct_max,
			load1_avg, load1_max,
			mem_total_max, mem_avail_avg, mem_avail_min,
			disk_total_max, disk_used_avg, disk_used_max,
			net_rx_bps_avg, net_rx_bps_max,
			net_tx_bps_avg, net_tx_bps_max,
			samples
		)
		SELECT
			org_id, host_id,
			date_bin('1 hour', ts, TIMESTAMPTZ 'epoch') AS bucket,
			(SUM(cpu_pct_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(cpu_pct_max)::real,
			(SUM(load1_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(load1_max)::real,
			MAX(mem_total_max),
			(SUM(mem_avail_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MIN(mem_avail_min)::real,
			MAX(disk_total_max),
			(SUM(disk_used_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(disk_used_max)::real,
			(SUM(net_rx_bps_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(net_rx_bps_max)::real,
			(SUM(net_tx_bps_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(net_tx_bps_max)::real,
			SUM(samples)::int
		FROM host_metrics_1m
		WHERE ts > $1 AND ts < $2
		GROUP BY org_id, host_id, bucket
		HAVING date_bin('1 hour', MIN(ts), TIMESTAMPTZ 'epoch') > $1
		   AND date_bin('1 hour', MIN(ts), TIMESTAMPTZ 'epoch') + interval '1 hour' <= $2
		ON CONFLICT (host_id, ts) DO UPDATE SET
			org_id = EXCLUDED.org_id,
			cpu_pct_avg = EXCLUDED.cpu_pct_avg,
			cpu_pct_max = EXCLUDED.cpu_pct_max,
			load1_avg = EXCLUDED.load1_avg,
			load1_max = EXCLUDED.load1_max,
			mem_total_max = EXCLUDED.mem_total_max,
			mem_avail_avg = EXCLUDED.mem_avail_avg,
			mem_avail_min = EXCLUDED.mem_avail_min,
			disk_total_max = EXCLUDED.disk_total_max,
			disk_used_avg = EXCLUDED.disk_used_avg,
			disk_used_max = EXCLUDED.disk_used_max,
			net_rx_bps_avg = EXCLUDED.net_rx_bps_avg,
			net_rx_bps_max = EXCLUDED.net_rx_bps_max,
			net_tx_bps_avg = EXCLUDED.net_tx_bps_avg,
			net_tx_bps_max = EXCLUDED.net_tx_bps_max,
			samples = EXCLUDED.samples`,
		from, to,
	)
	if err != nil {
		return fmt.Errorf("store: Rollup: %w", err)
	}
	return nil
}

func (s *Store) rollupContainer1h(ctx context.Context, now time.Time) error {
	to := now.Truncate(time.Hour)
	from := now.Add(-48 * time.Hour)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO container_metrics_1h (
			org_id, host_id, container_id, ts,
			cpu_pct_avg, cpu_pct_max,
			mem_usage_avg, mem_usage_max, mem_limit_max,
			net_rx_bps_avg, net_rx_bps_max,
			net_tx_bps_avg, net_tx_bps_max,
			blk_read_bps_avg, blk_read_bps_max,
			blk_write_bps_avg, blk_write_bps_max,
			pids_avg, pids_max,
			samples
		)
		SELECT
			org_id, host_id, container_id,
			date_bin('1 hour', ts, TIMESTAMPTZ 'epoch') AS bucket,
			(SUM(cpu_pct_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(cpu_pct_max)::real,
			(SUM(mem_usage_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(mem_usage_max)::real,
			MAX(mem_limit_max),
			(SUM(net_rx_bps_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(net_rx_bps_max)::real,
			(SUM(net_tx_bps_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(net_tx_bps_max)::real,
			(SUM(blk_read_bps_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(blk_read_bps_max)::real,
			(SUM(blk_write_bps_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(blk_write_bps_max)::real,
			(SUM(pids_avg * samples) / NULLIF(SUM(samples), 0))::real,
			MAX(pids_max)::real,
			SUM(samples)::int
		FROM container_metrics_1m
		WHERE ts > $1 AND ts < $2
		GROUP BY org_id, host_id, container_id, bucket
		HAVING date_bin('1 hour', MIN(ts), TIMESTAMPTZ 'epoch') > $1
		   AND date_bin('1 hour', MIN(ts), TIMESTAMPTZ 'epoch') + interval '1 hour' <= $2
		ON CONFLICT (host_id, container_id, ts) DO UPDATE SET
			org_id = EXCLUDED.org_id,
			cpu_pct_avg = EXCLUDED.cpu_pct_avg,
			cpu_pct_max = EXCLUDED.cpu_pct_max,
			mem_usage_avg = EXCLUDED.mem_usage_avg,
			mem_usage_max = EXCLUDED.mem_usage_max,
			mem_limit_max = EXCLUDED.mem_limit_max,
			net_rx_bps_avg = EXCLUDED.net_rx_bps_avg,
			net_rx_bps_max = EXCLUDED.net_rx_bps_max,
			net_tx_bps_avg = EXCLUDED.net_tx_bps_avg,
			net_tx_bps_max = EXCLUDED.net_tx_bps_max,
			blk_read_bps_avg = EXCLUDED.blk_read_bps_avg,
			blk_read_bps_max = EXCLUDED.blk_read_bps_max,
			blk_write_bps_avg = EXCLUDED.blk_write_bps_avg,
			blk_write_bps_max = EXCLUDED.blk_write_bps_max,
			pids_avg = EXCLUDED.pids_avg,
			pids_max = EXCLUDED.pids_max,
			samples = EXCLUDED.samples`,
		from, to,
	)
	if err != nil {
		return fmt.Errorf("store: Rollup: %w", err)
	}
	return nil
}

// PruneRollups deletes 1m rows older than now-30d and 1h rows older than now-395d.
func (s *Store) PruneRollups(ctx context.Context, now time.Time) error {
	now = now.UTC()
	cutoff1m := now.Add(-Rollup1mRetention)
	cutoff1h := now.Add(-Rollup1hRetention)
	stmts := []struct {
		q   string
		arg time.Time
	}{
		{`DELETE FROM host_metrics_1m WHERE ts < $1`, cutoff1m},
		{`DELETE FROM container_metrics_1m WHERE ts < $1`, cutoff1m},
		{`DELETE FROM host_metrics_1h WHERE ts < $1`, cutoff1h},
		{`DELETE FROM container_metrics_1h WHERE ts < $1`, cutoff1h},
	}
	for _, st := range stmts {
		if _, err := s.pool.Exec(ctx, st.q, st.arg); err != nil {
			return fmt.Errorf("store: PruneRollups: %w", err)
		}
	}
	return nil
}

func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func pgQuoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
