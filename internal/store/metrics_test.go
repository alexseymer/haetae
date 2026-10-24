//go:build integration

package store_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
)

func enrollHost(t *testing.T, s *store.Store, orgID uuid.UUID, name string) store.Host {
	t.Helper()
	ctx := context.Background()
	tok, _, err := s.CreateEnrollmentToken(ctx, orgID, name, time.Hour)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	h, err := s.EnrollHost(ctx, tok, store.HostInfo{Hostname: name}, func(id uuid.UUID) (string, error) {
		return "serial-" + id.String(), nil
	})
	if err != nil {
		t.Fatalf("EnrollHost: %v", err)
	}
	return h
}

func TestReplaceContainersSnapshotAndOrgIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	orgA, err := s.CreateOrg(ctx, "containers-a")
	if err != nil {
		t.Fatalf("CreateOrg A: %v", err)
	}
	orgB, err := s.CreateOrg(ctx, "containers-b")
	if err != nil {
		t.Fatalf("CreateOrg B: %v", err)
	}
	hostA := enrollHost(t, s, orgA.ID, "host-a")
	_ = enrollHost(t, s, orgB.ID, "host-b")

	created := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	started := created.Add(time.Second)
	snap := []store.Container{
		{
			ContainerID: "c1", Name: "web-1", Stack: "app", Service: "web",
			Image: "nginx", ImageVersion: "1.27", State: "running", Status: "Up", Health: "healthy",
			RestartCount: 1, Ports: []store.Port{{IP: "0.0.0.0", Private: 80, Public: 8080, Proto: "tcp"}},
			CreatedAt: &created, StartedAt: &started,
		},
		{
			ContainerID: "c2", Name: "db-1", Stack: "app", Service: "db",
			Image: "postgres", State: "running", Status: "Up",
		},
	}
	if err := s.ReplaceContainers(ctx, orgA.ID, hostA.ID, snap, true); err != nil {
		t.Fatalf("ReplaceContainers: %v", err)
	}

	got, err := s.ListContainers(ctx, orgA.ID, hostA.ID)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 containers, got %d", len(got))
	}
	if got[0].Service != "db" || got[1].Service != "web" {
		t.Fatalf("order: %+v", got)
	}
	if got[1].ContainerID != "c1" || got[1].ImageVersion != "1.27" || got[1].RestartCount != 1 {
		t.Fatalf("web container: %+v", got[1])
	}
	if len(got[1].Ports) != 1 || got[1].Ports[0].Public != 8080 {
		t.Fatalf("ports: %+v", got[1].Ports)
	}

	// Second snapshot without c2 marks it removed.
	if err := s.ReplaceContainers(ctx, orgA.ID, hostA.ID, []store.Container{snap[0]}, true); err != nil {
		t.Fatalf("ReplaceContainers 2: %v", err)
	}
	got, err = s.ListContainers(ctx, orgA.ID, hostA.ID)
	if err != nil {
		t.Fatalf("ListContainers after remove: %v", err)
	}
	if len(got) != 1 || got[0].ContainerID != "c1" {
		t.Fatalf("after remove: %+v", got)
	}

	// Org B cannot list org A's containers.
	cross, err := s.ListContainers(ctx, orgB.ID, hostA.ID)
	if err != nil {
		t.Fatalf("ListContainers cross-org: %v", err)
	}
	if len(cross) != 0 {
		t.Fatalf("org B listed org A containers: %+v", cross)
	}
}

func TestInsertAndSeriesRaw(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "metrics-raw")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	h := enrollHost(t, s, org.ID, "mhost")

	now := time.Date(2026, 10, 24, 15, 30, 0, 0, time.UTC)
	if err := s.EnsurePartitions(ctx, now); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}

	base := now.Add(-30 * time.Minute)
	hostMs := []store.HostMetric{
		{TS: base, CPUPct: 10, Load1: 0.5, MemTotal: 1000, MemAvail: 400, DiskTotal: 5000, DiskUsed: 1000, NetRxBps: 1, NetTxBps: 2},
		{TS: base.Add(10 * time.Second), CPUPct: 20, Load1: 0.7, MemTotal: 1000, MemAvail: 300, DiskTotal: 5000, DiskUsed: 1100, NetRxBps: 3, NetTxBps: 4},
		{TS: base.Add(20 * time.Second), CPUPct: 30, Load1: 0.9, MemTotal: 1000, MemAvail: 200, DiskTotal: 5000, DiskUsed: 1200, NetRxBps: 5, NetTxBps: 6},
	}
	if err := s.InsertHostMetrics(ctx, org.ID, h.ID, hostMs); err != nil {
		t.Fatalf("InsertHostMetrics: %v", err)
	}
	ctrMs := []store.ContainerMetric{
		{ContainerID: "ctr1", TS: base, CPUPct: 5, MemUsage: 100, MemLimit: 512, NetRxBps: 1, NetTxBps: 1, BlkReadBps: 0, BlkWriteBps: 0, Pids: 3},
		{ContainerID: "ctr1", TS: base.Add(10 * time.Second), CPUPct: 15, MemUsage: 200, MemLimit: 512, NetRxBps: 2, NetTxBps: 2, BlkReadBps: 1, BlkWriteBps: 1, Pids: 4},
	}
	if err := s.InsertContainerMetrics(ctx, org.ID, h.ID, ctrMs); err != nil {
		t.Fatalf("InsertContainerMetrics: %v", err)
	}

	from, to := base.Add(-time.Minute), base.Add(time.Hour)
	res, pts, err := s.HostSeries(ctx, org.ID, h.ID, "cpu_pct", from, to)
	if err != nil {
		t.Fatalf("HostSeries cpu: %v", err)
	}
	if res != store.ResolutionRaw {
		t.Fatalf("resolution: %s", res)
	}
	if len(pts) != 3 {
		t.Fatalf("cpu points: %d", len(pts))
	}
	for i, want := range []float64{10, 20, 30} {
		if pts[i].Value != want || pts[i].Max != want {
			t.Fatalf("cpu[%d]=%+v want %v", i, pts[i], want)
		}
		if i > 0 && !pts[i].TS.After(pts[i-1].TS) {
			t.Fatalf("not ordered: %v then %v", pts[i-1].TS, pts[i].TS)
		}
	}

	_, memPts, err := s.HostSeries(ctx, org.ID, h.ID, "mem_used", from, to)
	if err != nil {
		t.Fatalf("HostSeries mem_used: %v", err)
	}
	if len(memPts) != 3 || memPts[0].Value != 600 || memPts[1].Value != 700 || memPts[2].Value != 800 {
		t.Fatalf("mem_used: %+v", memPts)
	}

	if _, _, err := s.HostSeries(ctx, org.ID, h.ID, "nope", from, to); err == nil {
		t.Fatal("unknown host metric: want error")
	}

	cres, cpts, err := s.ContainerSeries(ctx, org.ID, h.ID, "ctr1", "cpu_pct", from, to)
	if err != nil {
		t.Fatalf("ContainerSeries: %v", err)
	}
	if cres != store.ResolutionRaw || len(cpts) != 2 || cpts[0].Value != 5 || cpts[1].Value != 15 {
		t.Fatalf("container series: res=%s pts=%+v", cres, cpts)
	}
	if _, _, err := s.ContainerSeries(ctx, org.ID, h.ID, "ctr1", "nope", from, to); err == nil {
		t.Fatal("unknown container metric: want error")
	}
}

func TestRollup1mIdempotent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "rollup-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	h := enrollHost(t, s, org.ID, "rollup-host")

	// now at :05 so minutes ending at :01,:02,:03,:04 are complete; put samples in :03.
	now := time.Date(2026, 10, 24, 12, 5, 0, 0, time.UTC)
	if err := s.EnsurePartitions(ctx, now); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	bucket := time.Date(2026, 10, 24, 12, 3, 0, 0, time.UTC)
	ms := []store.HostMetric{
		{TS: bucket.Add(10 * time.Second), CPUPct: 10, Load1: 1, MemTotal: 100, MemAvail: 40, DiskTotal: 200, DiskUsed: 50, NetRxBps: 1, NetTxBps: 2},
		{TS: bucket.Add(20 * time.Second), CPUPct: 30, Load1: 3, MemTotal: 100, MemAvail: 20, DiskTotal: 200, DiskUsed: 70, NetRxBps: 5, NetTxBps: 6},
	}
	if err := s.InsertHostMetrics(ctx, org.ID, h.ID, ms); err != nil {
		t.Fatalf("InsertHostMetrics: %v", err)
	}
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatalf("Rollup 2: %v", err)
	}

	var (
		avg, mx float64
		samples int
		ts      time.Time
	)
	err = s.Pool().QueryRow(ctx, `
		SELECT ts, cpu_pct_avg, cpu_pct_max, samples
		FROM host_metrics_1m WHERE org_id = $1 AND host_id = $2`,
		org.ID, h.ID,
	).Scan(&ts, &avg, &mx, &samples)
	if err != nil {
		t.Fatalf("query 1m: %v", err)
	}
	if !ts.Equal(bucket) {
		t.Fatalf("bucket ts: %v want %v", ts, bucket)
	}
	if samples != 2 {
		t.Fatalf("samples: %d", samples)
	}
	if math.Abs(avg-20) > 1e-3 || math.Abs(mx-30) > 1e-3 {
		t.Fatalf("avg=%v max=%v", avg, mx)
	}

	var n int
	err = s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM host_metrics_1m WHERE host_id = $1`, h.ID).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("idempotent count: %d", n)
	}
}

func TestSeries1hResolution(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "res-1h")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	h := enrollHost(t, s, org.ID, "res-host")

	// now at :05 so raw→1m covers minutes in hour 13; hour 13 is complete for 1m→1h.
	now := time.Date(2026, 10, 24, 14, 5, 0, 0, time.UTC)
	if err := s.EnsurePartitions(ctx, now); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}

	hour := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	var hostMs []store.HostMetric
	for i := 0; i < 6; i++ {
		hostMs = append(hostMs, store.HostMetric{
			TS:       hour.Add(time.Duration(i) * 10 * time.Minute).Add(5 * time.Second),
			CPUPct:   float64(10 + i),
			Load1:    1,
			MemTotal: 100, MemAvail: 50,
			DiskTotal: 200, DiskUsed: 10, NetRxBps: 1, NetTxBps: 1,
		})
	}
	if err := s.InsertHostMetrics(ctx, org.ID, h.ID, hostMs); err != nil {
		t.Fatalf("InsertHostMetrics: %v", err)
	}
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatalf("Rollup: %v", err)
	}

	// Span > 7 days selects 1h per ResolutionFor (raw ≤6h, 1m ≤7d, else 1h).
	from := now.Add(-8 * 24 * time.Hour)
	res, pts, err := s.HostSeries(ctx, org.ID, h.ID, "cpu_pct", from, now)
	if err != nil {
		t.Fatalf("HostSeries: %v", err)
	}
	if res != store.Resolution1h {
		t.Fatalf("want 1h resolution, got %s", res)
	}
	if len(pts) != 1 {
		t.Fatalf("want 1 rolled-up point, got %+v", pts)
	}
	if !pts[0].TS.Equal(hour) {
		t.Fatalf("bucket: %v want %v", pts[0].TS, hour)
	}
	wantAvg := (10.0 + 11 + 12 + 13 + 14 + 15) / 6
	if math.Abs(pts[0].Value-wantAvg) > 1e-2 {
		t.Fatalf("avg: %v want %v", pts[0].Value, wantAvg)
	}
	if math.Abs(pts[0].Max-15) > 1e-3 {
		t.Fatalf("max: %v", pts[0].Max)
	}
}

func TestEnsureAndDropPartitions(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

	if err := s.EnsurePartitions(ctx, now); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	if err := s.EnsurePartitions(ctx, now); err != nil {
		t.Fatalf("EnsurePartitions idempotent: %v", err)
	}

	// Create an old partition manually (4 days ago).
	oldDay := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	oldEnd := oldDay.AddDate(0, 0, 1)
	for _, parent := range []string{"host_metrics", "container_metrics"} {
		name := parent + "_p" + oldDay.Format("20060102")
		q := `CREATE TABLE IF NOT EXISTS "` + name + `" PARTITION OF "` + parent +
			`" FOR VALUES FROM ('` + oldDay.Format(time.RFC3339) + `') TO ('` + oldEnd.Format(time.RFC3339) + `')`
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("create old partition %s: %v", name, err)
		}
	}

	n, err := s.DropExpiredPartitions(ctx, now, 48*time.Hour)
	if err != nil {
		t.Fatalf("DropExpiredPartitions: %v", err)
	}
	if n != 2 {
		t.Fatalf("dropped: %d want 2", n)
	}

	var todayExists bool
	err = s.Pool().QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pg_class WHERE relname = 'host_metrics_p20261024'
		)`).Scan(&todayExists)
	if err != nil {
		t.Fatalf("check today: %v", err)
	}
	if !todayExists {
		t.Fatal("today's partition was dropped")
	}

	var oldExists bool
	err = s.Pool().QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pg_class WHERE relname = 'host_metrics_p20261020'
		)`).Scan(&oldExists)
	if err != nil {
		t.Fatalf("check old: %v", err)
	}
	if oldExists {
		t.Fatal("old partition still exists")
	}
}

func TestPruneRollups(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "prune-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	h := enrollHost(t, s, org.ID, "prune-host")
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

	old1m := now.Add(-31 * 24 * time.Hour)
	recent1m := now.Add(-2 * 24 * time.Hour)
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO host_metrics_1m (
			org_id, host_id, ts, cpu_pct_avg, cpu_pct_max, load1_avg, load1_max,
			mem_total_max, mem_avail_avg, mem_avail_min, disk_total_max, disk_used_avg, disk_used_max,
			net_rx_bps_avg, net_rx_bps_max, net_tx_bps_avg, net_tx_bps_max, samples
		) VALUES
		($1,$2,$3,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1),
		($1,$2,$4,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1)`,
		org.ID, h.ID, old1m, recent1m,
	)
	if err != nil {
		t.Fatalf("insert 1m: %v", err)
	}

	if err := s.PruneRollups(ctx, now); err != nil {
		t.Fatalf("PruneRollups: %v", err)
	}

	var n int
	err = s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM host_metrics_1m WHERE host_id = $1`, h.ID).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("after prune count=%d want 1", n)
	}
}

func TestSeriesOrgIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	orgA, err := s.CreateOrg(ctx, "series-a")
	if err != nil {
		t.Fatalf("CreateOrg A: %v", err)
	}
	orgB, err := s.CreateOrg(ctx, "series-b")
	if err != nil {
		t.Fatalf("CreateOrg B: %v", err)
	}
	h := enrollHost(t, s, orgA.ID, "iso-host")
	now := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	if err := s.EnsurePartitions(ctx, now); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	ts := now.Add(-10 * time.Minute)
	if err := s.InsertHostMetrics(ctx, orgA.ID, h.ID, []store.HostMetric{{
		TS: ts, CPUPct: 42, Load1: 1, MemTotal: 100, MemAvail: 50, DiskTotal: 200, DiskUsed: 10, NetRxBps: 1, NetTxBps: 1,
	}}); err != nil {
		t.Fatalf("InsertHostMetrics: %v", err)
	}
	if err := s.InsertContainerMetrics(ctx, orgA.ID, h.ID, []store.ContainerMetric{{
		ContainerID: "c", TS: ts, CPUPct: 7, MemUsage: 1, MemLimit: 2, Pids: 1,
	}}); err != nil {
		t.Fatalf("InsertContainerMetrics: %v", err)
	}

	from, to := ts.Add(-time.Minute), ts.Add(time.Hour)
	_, pts, err := s.HostSeries(ctx, orgB.ID, h.ID, "cpu_pct", from, to)
	if err != nil {
		t.Fatalf("HostSeries B: %v", err)
	}
	if len(pts) != 0 {
		t.Fatalf("org B got host points: %+v", pts)
	}
	_, cpts, err := s.ContainerSeries(ctx, orgB.ID, h.ID, "c", "cpu_pct", from, to)
	if err != nil {
		t.Fatalf("ContainerSeries B: %v", err)
	}
	if len(cpts) != 0 {
		t.Fatalf("org B got container points: %+v", cpts)
	}
}
