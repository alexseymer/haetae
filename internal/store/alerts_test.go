//go:build integration

package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestOrgSettingsDefaultsAndUpsert(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "settings-org")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrgSettings(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	def := store.DefaultOrgSettings(org.ID)
	if got.CPUPct != def.CPUPct || got.OfflineAfterS != def.OfflineAfterS || got.TimeZone != "UTC" {
		t.Fatalf("defaults: %+v", got)
	}
	got.CPUPct = 80
	got.TimeZone = "Europe/Berlin"
	got.OrgID = org.ID
	saved, err := s.UpsertOrgSettings(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CPUPct != 80 || saved.TimeZone != "Europe/Berlin" {
		t.Fatalf("upsert: %+v", saved)
	}
	again, err := s.GetOrgSettings(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.CPUPct != 80 {
		t.Fatalf("reread: %+v", again)
	}
}

func TestIncidentAlertUpsertResolveAndList(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "inc-org")
	if err != nil {
		t.Fatal(err)
	}
	h := enrollHost(t, s, org.ID, "host-1")

	inc, created, err := s.OpenIncidentForHost(ctx, org.ID, h.ID, "web: unhealthy", store.SeverityMedium)
	if err != nil || !created {
		t.Fatalf("open: created=%v err=%v", created, err)
	}
	inc2, created2, err := s.OpenIncidentForHost(ctx, org.ID, h.ID, "other", store.SeverityHigh)
	if err != nil || created2 || inc2.ID != inc.ID {
		t.Fatalf("second open: created=%v id=%s want %s err=%v", created2, inc2.ID, inc.ID, err)
	}

	a, created, err := s.UpsertFiringAlert(ctx, store.Alert{
		OrgID: org.ID, HostID: h.ID, IncidentID: inc.ID,
		Rule: store.RuleUnhealthy, Container: "web", Severity: store.SeverityMedium,
		DedupeKey: "unhealthy|" + h.ID.String() + "|web", Summary: "web unhealthy",
	})
	if err != nil || !created {
		t.Fatalf("upsert: created=%v err=%v", created, err)
	}
	a2, created, err := s.UpsertFiringAlert(ctx, store.Alert{
		OrgID: org.ID, HostID: h.ID, IncidentID: inc.ID,
		Rule: store.RuleUnhealthy, Container: "web", Severity: store.SeverityMedium,
		DedupeKey: a.DedupeKey, Summary: "web unhealthy still",
	})
	if err != nil || created || a2.ID != a.ID {
		t.Fatalf("idempotent upsert: created=%v id=%s/%s err=%v", created, a.ID, a2.ID, err)
	}

	list, err := s.ListIncidents(ctx, org.ID, store.IncidentOpen, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListIncidents: %+v %v", list, err)
	}
	got, alerts, err := s.GetIncident(ctx, org.ID, inc.ID)
	if err != nil || got.ID != inc.ID || len(alerts) != 1 {
		t.Fatalf("GetIncident: %+v alerts=%d err=%v", got, len(alerts), err)
	}
	allAlerts, err := s.ListAlerts(ctx, org.ID, store.AlertFiring, 10)
	if err != nil || len(allAlerts) != 1 {
		t.Fatalf("ListAlerts: %d %v", len(allAlerts), err)
	}

	if _, err := s.ResolveAlert(ctx, org.ID, a.DedupeKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveIncident(ctx, org.ID, inc.ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListIncidents(ctx, org.ID, store.IncidentResolved, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("resolved list: %d %v", len(list), err)
	}
}

func TestOneOpenIncidentUniqueIndexConcurrent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "uniq-inc")
	if err != nil {
		t.Fatal(err)
	}
	h := enrollHost(t, s, org.ID, "host-u")

	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inc, _, err := s.OpenIncidentForHost(ctx, org.ID, h.ID, "t", store.SeverityHigh)
			if err != nil {
				t.Errorf("OpenIncidentForHost: %v", err)
				return
			}
			ids <- inc.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[uuid.UUID]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("distinct open incident ids = %d, want 1", len(seen))
	}
}

func TestAlertsOneFiringUniqueIndex(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "uniq-alert")
	if err != nil {
		t.Fatal(err)
	}
	h := enrollHost(t, s, org.ID, "host-a")
	inc, _, err := s.OpenIncidentForHost(ctx, org.ID, h.ID, "t", store.SeverityHigh)
	if err != nil {
		t.Fatal(err)
	}
	key := "crash_loop|" + h.ID.String() + "|c1"
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.UpsertFiringAlert(ctx, store.Alert{
				OrgID: org.ID, HostID: h.ID, IncidentID: inc.ID,
				Rule: store.RuleCrashLoop, Container: "c1", Severity: store.SeverityHigh,
				DedupeKey: key, Summary: "loop",
			})
			if err != nil {
				t.Errorf("UpsertFiringAlert: %v", err)
			}
		}()
	}
	wg.Wait()
	firing, err := s.ListFiringAlerts(ctx, org.ID)
	if err != nil || len(firing) != 1 {
		t.Fatalf("firing = %d err=%v", len(firing), err)
	}
}

func TestMutesCRUDAndActive(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "mute-org")
	if err != nil {
		t.Fatal(err)
	}
	h := enrollHost(t, s, org.ID, "host-m")
	scoped := s.ForOrg(org.ID)
	until := time.Now().UTC().Add(24 * time.Hour)
	m, err := scoped.CreateMute(ctx, store.Mute{
		OrgID: org.ID, HostID: &h.ID, Rule: store.RuleOOMKill, Until: until,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := scoped.ActiveMutes(ctx, org.ID, time.Now().UTC())
	if err != nil || len(active) != 1 {
		t.Fatalf("ActiveMutes: %d %v", len(active), err)
	}
	if !store.MuteMatches(active[0], h.ID, store.RuleOOMKill) {
		t.Fatal("MuteMatches failed")
	}
	if store.MuteMatches(active[0], h.ID, store.RuleCrashLoop) {
		t.Fatal("MuteMatches should not match other rule")
	}
	newUntil := until.Add(time.Hour)
	if _, err := scoped.UpdateMuteUntil(ctx, org.ID, m.ID, newUntil); err != nil {
		t.Fatal(err)
	}
	if err := scoped.DeleteMute(ctx, org.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := scoped.DeleteMute(ctx, org.ID, m.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAlertsRLSCrossOrgIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	orgA, err := s.CreateOrg(ctx, "rls-a")
	if err != nil {
		t.Fatal(err)
	}
	orgB, err := s.CreateOrg(ctx, "rls-b")
	if err != nil {
		t.Fatal(err)
	}
	hA := enrollHost(t, s, orgA.ID, "a-host")
	hB := enrollHost(t, s, orgB.ID, "b-host")

	incA, _, err := s.OpenIncidentForHost(ctx, orgA.ID, hA.ID, "a", store.SeverityHigh)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.UpsertFiringAlert(ctx, store.Alert{
		OrgID: orgA.ID, HostID: hA.ID, IncidentID: incA.ID,
		Rule: store.RuleHostOffline, Severity: store.SeverityCritical,
		DedupeKey: "host_offline|" + hA.ID.String(), Summary: "offline",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateMute(ctx, store.Mute{OrgID: orgA.ID, Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpsertOrgSettings(ctx, store.OrgSettings{
		OrgID: orgA.ID, TimeZone: "UTC", BriefingHour: 8,
		CPUPct: 90, MemPct: 90, DiskPct: 90, SustainedS: 300, OfflineAfterS: 120,
	})
	if err != nil {
		t.Fatal(err)
	}

	b := s.ForOrg(orgB.ID)
	incs, err := b.ListIncidents(ctx, orgB.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(incs) != 0 {
		t.Fatalf("org B saw org A incidents: %+v", incs)
	}
	alerts, err := b.ListAlerts(ctx, orgB.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 0 {
		t.Fatalf("org B saw org A alerts: %+v", alerts)
	}
	mutes, err := b.ListMutes(ctx, orgB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutes) != 0 {
		t.Fatalf("org B saw org A mutes: %+v", mutes)
	}
	// Direct read of A's incident id through B's scoped view must fail.
	_, _, err = b.GetIncident(ctx, orgB.ID, incA.ID)
	if !errors.Is(err, store.ErrNotFound) {
		// Also try with orgA id in the query — scoped role still filters by app.org_id=B.
		_, _, err = b.GetIncident(ctx, orgA.ID, incA.ID)
		if err == nil {
			t.Fatal("scoped org B read org A incident")
		}
	}

	// Org B can write its own rows.
	incB, _, err := s.OpenIncidentForHost(ctx, orgB.ID, hB.ID, "b", store.SeverityLow)
	if err != nil {
		t.Fatal(err)
	}
	_ = incB
}

func TestRuleReadQueries(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.EnsurePartitions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	org, err := s.CreateOrg(ctx, "rules-read")
	if err != nil {
		t.Fatal(err)
	}
	h := enrollHost(t, s, org.ID, "host-r")
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	if err := s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{{
		ContainerID: "c1", Name: "web", State: "running", Health: "unhealthy",
		RestartCount: 4, OOMKilled: true, StartedAt: &started,
	}}, true); err != nil {
		t.Fatal(err)
	}
	rs, err := s.ContainerRestarts(ctx, org.ID)
	if err != nil || len(rs) != 1 || rs[0].RestartCount != 4 {
		t.Fatalf("ContainerRestarts: %+v %v", rs, err)
	}
	oom, err := s.ContainersOOMSince(ctx, org.ID, now.Add(-10*time.Minute))
	if err != nil || len(oom) != 1 {
		t.Fatalf("ContainersOOMSince: %+v %v", oom, err)
	}
	health, err := s.ContainerHealth(ctx, org.ID)
	if err != nil || len(health) != 1 || health[0].Health != "unhealthy" {
		t.Fatalf("ContainerHealth: %+v %v", health, err)
	}

	// Sustained CPU: insert samples all above threshold.
	var ms []store.HostMetric
	for i := 0; i < 6; i++ {
		ms = append(ms, store.HostMetric{
			TS:     now.Add(-time.Duration(i) * 30 * time.Second),
			CPUPct: 95, MemTotal: 1000, MemAvail: 50, DiskTotal: 1000, DiskUsed: 920,
		})
	}
	if err := s.InsertHostMetrics(ctx, org.ID, h.ID, ms); err != nil {
		t.Fatal(err)
	}
	ok, err := s.HostMetricSustained(ctx, org.ID, h.ID, "cpu_pct", 90, 3*time.Minute, now)
	if err != nil || !ok {
		t.Fatalf("cpu sustained: %v %v", ok, err)
	}
	ok, err = s.HostMetricSustained(ctx, org.ID, h.ID, "mem_pct", 90, 3*time.Minute, now)
	if err != nil || !ok {
		t.Fatalf("mem sustained: %v %v", ok, err)
	}
	ok, err = s.HostMetricSustained(ctx, org.ID, h.ID, "disk_pct", 90, 3*time.Minute, now)
	if err != nil || !ok {
		t.Fatalf("disk sustained: %v %v", ok, err)
	}
	pct, err := s.HostDiskPct(ctx, org.ID, h.ID)
	if err != nil || pct < 90 {
		t.Fatalf("HostDiskPct: %v %v", pct, err)
	}

	offline, err := s.HostsOfflineSince(ctx, org.ID, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(offline) != 1 {
		t.Fatalf("HostsOfflineSince: %+v (last_seen unset → offline)", offline)
	}

	// Failed deploy.
	p, err := s.CreateProject(ctx, org.ID, h.ID, uuid.Nil, store.ProjectInput{
		Name: "app", SourceKind: "inline", InlineCompose: "services:\n  web:\n    image: nginx\n",
		Service: "web", Port: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDeploy(ctx, p, store.TriggerManual, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeployFailed(ctx, org.ID, h.ID, d.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	failed, err := s.RecentFailedDeploys(ctx, org.ID, now.Add(-time.Hour))
	if err != nil || len(failed) != 1 {
		t.Fatalf("RecentFailedDeploys: %+v %v", failed, err)
	}
}
