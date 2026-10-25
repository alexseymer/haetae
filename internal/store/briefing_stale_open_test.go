//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestBriefingFactsIncludesStaleOpenIncidents(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedBriefingOrg(t, s, "stale-open")
	old := time.Now().UTC().Add(-72 * time.Hour)
	inc, err := s.InsertIncidentForTest(ctx, store.Incident{
		OrgID: org.ID, HostID: h.ID, Title: "stale-open-title",
		Severity: store.SeverityHigh, State: "open",
		OpenedAt: old, UpdatedAt: old,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.InsertAlertForTest(ctx, store.Alert{
		OrgID: org.ID, HostID: h.ID, IncidentID: inc.ID,
		Rule: "oom_kill", Severity: store.SeverityHigh, State: "firing",
		DedupeKey: "oom|" + h.ID.String(), Summary: "oom",
		FirstAt: old, LastAt: old,
	})
	if err != nil {
		t.Fatal(err)
	}

	since := time.Now().Add(-24 * time.Hour)
	f, err := s.BriefingFacts(ctx, org.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	var sawOpen bool
	for _, it := range f.Items {
		if it.Kind == "incident" && it.Title == "stale-open-title" {
			sawOpen = true
			if it.At.IsZero() {
				t.Fatal("incident At unset")
			}
		}
	}
	if !sawOpen {
		t.Fatalf("stale open incident missing from briefing facts: %+v", f.Items)
	}
	// Firing alert with last_at outside window should not appear.
	for _, it := range f.Items {
		if it.Kind == "alert" && it.Title == "oom" {
			t.Fatal("stale firing alert should be window-filtered")
		}
	}
}
