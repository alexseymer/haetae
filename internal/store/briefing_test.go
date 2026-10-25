//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func seedBriefingOrg(t *testing.T, s *store.Store, name string) (store.Org, store.Host) {
	t.Helper()
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, name+"-host", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.EnrollHost(ctx, tok, store.HostInfo{Hostname: name}, func(uuid.UUID) (string, error) {
		return "serial-" + name, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return org, h
}

func insertIncidentAlert(t *testing.T, s *store.Store, orgID, hostID uuid.UUID, title, sev string) {
	t.Helper()
	ctx := context.Background()
	var incID uuid.UUID
	err := s.QueryRowForTest(ctx, `
		INSERT INTO incidents (org_id, host_id, title, severity, state)
		VALUES ($1, $2, $3, $4, 'open')
		RETURNING id`,
		orgID, hostID, title, sev,
	).Scan(&incID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO alerts (
			org_id, host_id, incident_id, rule, container, severity, state,
			dedupe_key, summary
		) VALUES ($1, $2, $3, 'oom_kill', 'web', $4, 'firing', $5, $6)`,
		orgID, hostID, incID, sev, "oom_kill|"+hostID.String()+"|"+title, title+" summary",
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBriefingFactsCountsAndItems(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedBriefingOrg(t, s, "facts-a")
	if err := s.MarkConnected(ctx, h.ID, store.HostInfo{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{
		{ContainerID: "c1", Name: "web", State: "running"},
		{ContainerID: "c2", Name: "db", State: "exited"},
	}, true); err != nil {
		t.Fatal(err)
	}
	insertIncidentAlert(t, s, org.ID, h.ID, "web: oom on facts-a-host", store.SeverityHigh)

	since := time.Now().Add(-24 * time.Hour)
	f, err := s.BriefingFacts(ctx, org.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	if f.Hosts != 1 || f.Online != 1 {
		t.Fatalf("hosts=%d online=%d; want 1, 1", f.Hosts, f.Online)
	}
	if f.Containers["running"] != 1 || f.Containers["exited"] != 1 {
		t.Fatalf("containers=%v", f.Containers)
	}
	if len(f.Items) < 2 {
		t.Fatalf("items=%d; want incident + alert", len(f.Items))
	}
	var sawInc, sawAlert bool
	for _, it := range f.Items {
		if it.HostName != "facts-a-host" {
			t.Fatalf("host name %q", it.HostName)
		}
		switch it.Kind {
		case "incident":
			sawInc = true
			if it.Title != "web: oom on facts-a-host" || it.Severity != store.SeverityHigh {
				t.Fatalf("incident item: %+v", it)
			}
		case "alert":
			sawAlert = true
		}
	}
	if !sawInc || !sawAlert {
		t.Fatalf("items=%+v; want both kinds", f.Items)
	}
}

func TestBriefingFactsTenantIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	orgA, hostA := seedBriefingOrg(t, s, "iso-a")
	orgB, hostB := seedBriefingOrg(t, s, "iso-b")
	insertIncidentAlert(t, s, orgA.ID, hostA.ID, "a-only", store.SeverityCritical)
	insertIncidentAlert(t, s, orgB.ID, hostB.ID, "b-only", store.SeverityCritical)

	since := time.Now().Add(-24 * time.Hour)
	fa, err := s.BriefingFacts(ctx, orgA.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := s.BriefingFacts(ctx, orgB.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range fa.Items {
		if it.Title == "b-only" || it.Title == "b-only summary" {
			t.Fatalf("org A saw org B item: %+v", it)
		}
	}
	for _, it := range fb.Items {
		if it.Title == "a-only" || it.Title == "a-only summary" {
			t.Fatalf("org B saw org A item: %+v", it)
		}
	}

	scoped := s.ForOrg(orgA.ID)
	fs, err := scoped.BriefingFacts(ctx, orgA.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.Items) == 0 {
		t.Fatal("scoped BriefingFacts returned no items for own org")
	}
	fsB, err := scoped.BriefingFacts(ctx, orgB.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(fsB.Items) != 0 || fsB.Hosts != 0 {
		t.Fatalf("scoped as A reading B: hosts=%d items=%d", fsB.Hosts, len(fsB.Items))
	}
}

func TestBriefingTryClaimOnce(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _ := seedBriefingOrg(t, s, "claim")
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)

	ok, err := s.BriefingTryClaim(ctx, org.ID, day)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	ok, err = s.BriefingTryClaim(ctx, org.ID, day)
	if err != nil || ok {
		t.Fatalf("second claim: ok=%v err=%v; want false, nil", ok, err)
	}
	if err := s.BriefingDelete(ctx, org.ID, day); err != nil {
		t.Fatal(err)
	}
	ok, err = s.BriefingTryClaim(ctx, org.ID, day)
	if err != nil || !ok {
		t.Fatalf("reclaim after delete: ok=%v err=%v", ok, err)
	}
}

func TestBriefingOrgsDefaults(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _ := seedBriefingOrg(t, s, "defaults")
	orgs, err := s.BriefingOrgs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range orgs {
		if o.OrgID == org.ID {
			found = true
			if o.TimeZone != "UTC" || o.BriefingHour != 8 {
				t.Fatalf("defaults: %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("org missing from BriefingOrgs")
	}
}
