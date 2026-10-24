//go:build integration

package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

type twoOrgs struct {
	s            *store.Store
	orgA, orgB   store.Org
	hostA, hostB store.Host
}

func seedTwoOrgs(t *testing.T) twoOrgs {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.EnsurePartitions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var o twoOrgs
	o.s = s
	mk := func(name string) (store.Org, store.Host) {
		org, err := s.CreateOrg(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, name+"-host", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		h, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "serial-" + name, nil })
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		// Half an hour into the previous hour: inside the rollup lookback and in
		// a complete minute and hour, so every rollup table gets rows.
		past := now.Truncate(time.Hour).Add(-30 * time.Minute)
		if err := s.InsertHostMetrics(ctx, org.ID, h.ID, []store.HostMetric{{TS: now, CPUPct: 1}, {TS: past, CPUPct: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertContainerMetrics(ctx, org.ID, h.ID, []store.ContainerMetric{{ContainerID: name + "-c", TS: past, CPUPct: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetLogStats(ctx, org.ID, h.ID, store.LogStats{LinesIn: 1}); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{{ContainerID: name + "-c", Name: "c"}}, true); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertLogEvents(ctx, org.ID, h.ID, []store.LogEvent{ev(name+"-t", store.LevelError, 1, now)}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateUser(ctx, org.ID, name+"@example.com", "a-long-enough-password", "admin"); err != nil {
			t.Fatal(err)
		}
		return org, h
	}
	o.orgA, o.hostA = mk("a")
	o.orgB, o.hostB = mk("b")
	if err := s.Rollup(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	return o
}

// tenantTables are all tables holding tenant data; each must show an
// organisation only its own rows, whatever the query.
var tenantTables = []string{
	"hosts", "enrollment_tokens", "containers", "host_metrics", "container_metrics",
	"host_metrics_1m", "host_metrics_1h", "container_metrics_1m", "container_metrics_1h",
	"log_templates", "log_events", "log_stats", "users", "sessions", "orgs",
}

// A query with no WHERE clause at all, run as the scoped role, returns only
// the organisation's own rows from every tenant table.
func TestRowLevelSecurityHidesOtherOrganisations(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	a := o.s.ForOrg(o.orgA.ID)

	for _, table := range tenantTables {
		idCol := "org_id"
		if table == "orgs" {
			idCol = "id"
		}
		// Through the scoped view (the same path the web API uses).
		rows, err := scopedQuery(ctx, a, "SELECT count(*), count(*) FILTER (WHERE "+idCol+" <> $1) FROM "+table, o.orgA.ID)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		total, other := rows[0], rows[1]
		if total == 0 && table != "sessions" && table != "enrollment_tokens" {
			t.Errorf("%s: the scoped role sees none of its own rows; the isolation test would be vacuous", table)
		}
		if other != 0 {
			t.Errorf("%s: scoped role sees %d rows of another organisation", table, other)
		}
		// Sanity: the unscoped connection sees both organisations on seeded tables.
		var all int
		_ = o.s.Pool().QueryRow(ctx, "SELECT count(DISTINCT "+idCol+") FROM "+table).Scan(&all)
		if table != "sessions" && table != "enrollment_tokens" && all < 2 {
			t.Errorf("%s: test data missing (unscoped sees %d organisations)", table, all)
		}
	}
}

// scopedQuery runs a query that returns two integers through a scoped store.
func scopedQuery(ctx context.Context, s *store.Store, sql string, args ...any) ([]int, error) {
	var a, b int
	if err := s.QueryRowForTest(ctx, sql, args...).Scan(&a, &b); err != nil {
		return nil, err
	}
	return []int{a, b}, nil
}

func TestScopedStoreRefusesCrossOrganisationAccess(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	a := o.s.ForOrg(o.orgA.ID)

	// The normal store methods, asked for organisation B's data, find nothing.
	if hosts, err := a.ListHosts(ctx, o.orgB.ID); err != nil || len(hosts) != 0 {
		t.Fatalf("ListHosts(B) as A = %v, %v; want nothing", hosts, err)
	}
	if _, err := a.GetHost(ctx, o.orgB.ID, o.hostB.ID); err == nil {
		t.Fatal("GetHost(B) as A succeeded")
	}
	if cs, _ := a.ListContainers(ctx, o.orgB.ID, o.hostB.ID); len(cs) != 0 {
		t.Fatalf("ListContainers(B) as A = %v", cs)
	}
	if evs, _ := a.ListLogEvents(ctx, o.orgB.ID, o.hostB.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 0, 10); len(evs) != 0 {
		t.Fatalf("ListLogEvents(B) as A = %v", evs)
	}
	if _, pts, _ := a.HostSeries(ctx, o.orgB.ID, o.hostB.ID, "cpu_pct", time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); len(pts) != 0 {
		t.Fatalf("HostSeries(B) as A = %v", pts)
	}
	// ...and the same calls for its own organisation work.
	if hosts, err := a.ListHosts(ctx, o.orgA.ID); err != nil || len(hosts) != 1 {
		t.Fatalf("ListHosts(A) as A = %v, %v", hosts, err)
	}
	if _, pts, err := a.HostSeries(ctx, o.orgA.ID, o.hostA.ID, "cpu_pct", time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err != nil || len(pts) != 1 {
		t.Fatalf("HostSeries(A) as A = %v, %v", pts, err)
	}
}

func TestScopedRoleCannotWriteOtherOrganisationsRows(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	a := o.s.ForOrg(o.orgA.ID)

	// Creating an enrollment token for organisation B violates the policy.
	if _, _, err := a.CreateEnrollmentToken(ctx, o.orgB.ID, "evil", time.Hour); err == nil {
		t.Fatal("organisation A created an enrollment token for organisation B")
	}
	// And for its own organisation it works.
	if _, _, err := a.CreateEnrollmentToken(ctx, o.orgA.ID, "ok", time.Hour); err != nil {
		t.Fatalf("own enrollment token: %v", err)
	}
	// Creating a user in another organisation is refused too.
	if _, err := a.CreateUser(ctx, o.orgB.ID, "intruder@example.com", "a-long-enough-password", "admin"); err == nil {
		t.Fatal("organisation A created a user in organisation B")
	}

	// Tables the role may only read refuse writes outright, even to its own rows.
	for _, stmt := range []string{
		"UPDATE hosts SET name = 'pwned'",
		"DELETE FROM hosts",
		"DELETE FROM log_events",
		"UPDATE containers SET name = 'x'",
		"INSERT INTO log_events (org_id, host_id, container_id, template_id, level, sample, count, first_ts, last_ts) SELECT org_id, id, 'c', 't', 1, 's', 1, now(), now() FROM hosts",
	} {
		if err := a.ExecForTest(ctx, stmt); err == nil {
			t.Errorf("scoped role was allowed: %s", stmt)
		} else if !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("%s: unexpected error %v", stmt, err)
		}
	}
	// Partition tables are not reachable directly.
	if err := a.ExecForTest(ctx, "SELECT count(*) FROM host_metrics_p"+time.Now().UTC().Format("20060102")); err == nil {
		t.Error("scoped role read a metrics partition directly")
	}

	// Nothing of B changed.
	hosts, err := o.s.ListHosts(ctx, o.orgB.ID)
	if err != nil || len(hosts) != 1 || hosts[0].Name != "b-host" {
		t.Fatalf("organisation B's hosts changed: %v, %v", hosts, err)
	}
}

// Without a scope the role sees nothing, instead of everything.
func TestScopeIsFailClosed(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	// A scope for an organisation that does not exist sees no rows anywhere.
	ghost := o.s.ForOrg(uuid.New())
	for _, table := range []string{"hosts", "users", "log_events", "host_metrics"} {
		rows, err := scopedQuery(ctx, ghost, "SELECT count(*), 0 FROM "+table)
		if err != nil || rows[0] != 0 {
			t.Errorf("%s: %v rows for an unknown organisation, err %v", table, rows, err)
		}
	}
}
