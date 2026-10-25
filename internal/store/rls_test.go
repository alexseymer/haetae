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
	"log_templates", "log_events", "log_stats", "users", "orgs",
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
		if total == 0 && table != "enrollment_tokens" {
			t.Errorf("%s: the scoped role sees none of its own rows; the isolation test would be vacuous", table)
		}
		if other != 0 {
			t.Errorf("%s: scoped role sees %d rows of another organisation", table, other)
		}
		// Sanity: the unscoped connection sees both organisations on seeded tables.
		var all int
		_ = o.s.Pool().QueryRow(ctx, "SELECT count(DISTINCT "+idCol+") FROM "+table).Scan(&all)
		if table != "enrollment_tokens" && all < 2 {
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
	if _, pts, _ := a.HostSeries(ctx, o.orgB.ID, o.hostB.ID, "cpu_pct", time.Now().Add(-10*time.Minute), time.Now().Add(time.Hour)); len(pts) != 0 {
		t.Fatalf("HostSeries(B) as A = %v", pts)
	}
	// ...and the same calls for its own organisation work.
	if hosts, err := a.ListHosts(ctx, o.orgA.ID); err != nil || len(hosts) != 1 {
		t.Fatalf("ListHosts(A) as A = %v, %v", hosts, err)
	}
	if _, pts, err := a.HostSeries(ctx, o.orgA.ID, o.hostA.ID, "cpu_pct", time.Now().Add(-10*time.Minute), time.Now().Add(time.Hour)); err != nil || len(pts) != 1 {
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

// The restricted role has no access to secrets at all: not to sessions, not to
// password hashes, not to enrollment token hashes, and it cannot change users.
func TestRestrictedRoleCannotReachSecrets(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	a := o.s.ForOrg(o.orgA.ID)

	// Make sure there is something to protect.
	if err := o.s.CreateSession(ctx, uuid.New(), o.orgA.ID, []byte("hash"), time.Now().Add(time.Hour)); err == nil {
		t.Log("unexpected: session for unknown user was accepted")
	}
	u, err := o.s.UserByEmail(ctx, "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.s.CreateSession(ctx, u.ID, o.orgA.ID, []byte("a-session-hash"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		"SELECT * FROM sessions",
		"SELECT token_hash FROM sessions",
		"SELECT count(*) FROM sessions",
		"SELECT password_hash FROM users",
		"SELECT * FROM users",
		"SELECT token_hash FROM enrollment_tokens",
		"SELECT * FROM enrollment_tokens",
		"UPDATE users SET role = 'admin'",
		"UPDATE users SET password_hash = 'x'",
		"DELETE FROM users",
		"DELETE FROM sessions",
		"INSERT INTO sessions (token_hash, user_id, org_id, expires_at) SELECT 'x'::bytea, id, org_id, now() FROM users",
	} {
		err := a.ExecForTest(ctx, stmt)
		if err == nil {
			t.Errorf("the restricted role was allowed: %s", stmt)
		} else if !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}

	// What it may read is the safe columns.
	var n int
	if err := a.QueryRowForTest(ctx, "SELECT count(email), count(role) FROM users").Scan(&n, new(int)); err != nil || n != 1 {
		t.Fatalf("reading safe user columns: n=%d err=%v", n, err)
	}
	// The users it lists never carry a hash.
	users, err := a.ListUsers(ctx, o.orgA.ID)
	if err != nil || len(users) != 1 || users[0].PasswordHash != "" {
		t.Fatalf("ListUsers = %+v, %v", users, err)
	}
	created, err := a.CreateUser(ctx, o.orgA.ID, "new@example.com", "a-long-enough-password", "member")
	if err != nil || created.PasswordHash != "" {
		t.Fatalf("CreateUser = %+v, %v", created, err)
	}
	// ...and the server's own role still sees the hash it needs to sign people in.
	if full, err := o.s.UserByEmail(ctx, "new@example.com"); err != nil || full.PasswordHash == "" {
		t.Fatalf("UserByEmail lost the hash: %+v %v", full, err)
	}
}

// A request cancelled mid-flight must not leave a connection stuck in an open
// transaction or with the restricted role still set.
func TestCancelledRequestsDoNotPoisonThePool(t *testing.T) {
	o := seedTwoOrgs(t)
	a := o.s.ForOrg(o.orgA.ID)
	for range 40 {
		ctx, cancel := context.WithCancel(context.Background())
		rows, err := a.QueryRowForTest(ctx, "SELECT count(*) FROM hosts"), error(nil)
		_ = err
		cancel() // cancelled before and while the row is read
		var n int
		_ = rows.Scan(&n)
		_ = a.ExecForTest(ctx, "SELECT 1") // fails: context already cancelled
	}
	// The pool still works, still scoped, and nothing is left open.
	var n int
	if err := a.QueryRowForTest(context.Background(), "SELECT count(*) FROM hosts").Scan(&n); err != nil || n != 1 {
		t.Fatalf("after cancelled requests: n=%d err=%v", n, err)
	}
	var idle, active int
	if err := o.s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE state = 'idle in transaction'), count(*) FILTER (WHERE state = 'active' AND pid <> pg_backend_pid())
		 FROM pg_stat_activity WHERE datname = current_database()`).Scan(&idle, &active); err != nil {
		t.Fatal(err)
	}
	if idle != 0 {
		t.Fatalf("%d connections left idle in a transaction", idle)
	}
	// A plain query on the owner pool must not see a leftover restricted role.
	var role string
	if err := o.s.Pool().QueryRow(context.Background(), "SELECT current_user").Scan(&role); err != nil || role == store.AppRole {
		t.Fatalf("a pooled connection kept role %q (err %v)", role, err)
	}
}

// Changing a password and ending the other sessions are one step.
func TestChangePasswordEndsOtherSessionsAtomically(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	u, err := o.s.UserByEmail(ctx, "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"keep", "other1", "other2"} {
		if err := o.s.CreateSession(ctx, u.ID, o.orgA.ID, []byte(h), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.s.ChangePassword(ctx, o.orgA.ID, u.ID, "new-hash", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = o.s.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, u.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("%d sessions left, want only the one making the change", n)
	}
	if got, _ := o.s.UserByEmail(ctx, "a@example.com"); got.PasswordHash != "new-hash" {
		t.Fatal("password hash not changed")
	}
	// Another organisation's user cannot be changed by naming the wrong org.
	if err := o.s.ChangePassword(ctx, o.orgB.ID, u.ID, "evil", []byte("x")); err == nil {
		t.Fatal("ChangePassword with the wrong organisation succeeded")
	}
	if got, _ := o.s.UserByEmail(ctx, "a@example.com"); got.PasswordHash != "new-hash" {
		t.Fatal("password changed through the wrong organisation")
	}
}
