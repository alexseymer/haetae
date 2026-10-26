//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

func TestOperatorSettingsDefaultsAndRoundTrip(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	org, err := o.s.CreateOrg(ctx, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	got, err := o.s.GetOperatorSettings(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AIEnabled || got.Autonomy != "report" || got.DailyTokenCap != 200000 {
		t.Fatalf("defaults = %+v", got)
	}
	want := store.OperatorSettings{AIEnabled: true, Autonomy: "act", DailyTokenCap: 5}
	if err := o.s.SetOperatorSettings(ctx, org.ID, want); err != nil {
		t.Fatal(err)
	}
	if got, _ = o.s.GetOperatorSettings(ctx, org.ID); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if err := o.s.SetOperatorSettings(ctx, org.ID, store.OperatorSettings{Autonomy: "root"}); err == nil {
		t.Fatal("bad autonomy accepted")
	}
}

func TestOperatorUsageUpsertPerUTCDay(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	day := time.Date(2030, 1, 2, 23, 59, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := o.s.AddUsage(ctx, o.orgA.ID, day, 10, 5); err != nil {
			t.Fatal(err)
		}
	}
	u, err := o.s.UsageToday(ctx, o.orgA.ID, day)
	if err != nil || u.InputTokens != 20 || u.OutputTokens != 10 || u.Calls != 2 || u.Total() != 30 {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	if u, _ = o.s.UsageToday(ctx, o.orgA.ID, day.Add(2*time.Minute)); u.Total() != 0 {
		t.Fatalf("next UTC day should start empty: %+v", u)
	}
	if u, _ = o.s.UsageToday(ctx, o.orgB.ID, day); u.Total() != 0 {
		t.Fatalf("other org: %+v", u)
	}
}

func TestOperatorEvidenceRoundTripAndUniqueSeq(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	conv := uuid.New()
	mk := func(seq int) store.AIEvidence {
		return store.AIEvidence{OrgID: o.orgA.ID, ConversationID: conv, Seq: seq, Kind: "logs", Title: "t",
			Data: []byte(`{"untrusted":{"lines":["x"]}}`), Link: "/hosts/x?tab=logs"}
	}
	if _, err := o.s.InsertEvidence(ctx, mk(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.s.InsertEvidence(ctx, mk(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.s.InsertEvidence(ctx, mk(1)); err == nil {
		t.Fatal("duplicate (conversation, seq) accepted")
	}
	evs, err := o.s.GetEvidence(ctx, o.orgA.ID, conv)
	if err != nil || len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 || evs[0].Trusted {
		t.Fatalf("evidence = %+v, %v", evs, err)
	}
	if other, _ := o.s.GetEvidence(ctx, o.orgB.ID, conv); len(other) != 0 {
		t.Fatal("other org read evidence")
	}
}

func TestOperatorActionLifecycle(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	ev, err := o.s.InsertEvidence(ctx, store.AIEvidence{OrgID: o.orgA.ID, ConversationID: uuid.New(), Seq: 1, Kind: "containers", Trusted: true, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := o.s.InsertAction(ctx, store.AIAction{OrgID: o.orgA.ID, HostID: &o.hostA.ID, ContainerID: "a-c",
		Level: "suggest", Origin: "proposed", Outcome: "awaiting_approval", Reason: "r", EvidenceIDs: []uuid.UUID{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := o.s.GetAction(ctx, o.orgA.ID, a.ID)
	if err != nil || got.Outcome != "awaiting_approval" || len(got.EvidenceIDs) != 1 || got.EvidenceIDs[0] != ev.ID {
		t.Fatalf("action = %+v, %v", got, err)
	}
	if _, err := o.s.GetAction(ctx, o.orgB.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign GetAction err = %v", err)
	}
	if _, err := o.s.ClaimApprovedAction(ctx, o.orgB.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign claim err = %v", err)
	}
	c, err := o.s.ClaimApprovedAction(ctx, o.orgA.ID, a.ID)
	if err != nil || c.Outcome != "sent" || c.Origin != "approved" {
		t.Fatalf("claim = %+v, %v", c, err)
	}
	if _, err := o.s.ClaimApprovedAction(ctx, o.orgA.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second claim err = %v", err)
	}
	if err := o.s.UpdateActionOutcome(ctx, o.orgA.ID, a.ID, "succeeded", "ok"); err != nil {
		t.Fatal(err)
	}
	if n, _ := o.s.CountRecentRestarts(ctx, o.orgA.ID, "a-c", time.Now().Add(-time.Hour)); n != 1 {
		t.Fatalf("recent restarts = %d", n)
	}
	if n, _ := o.s.CountRecentRestarts(ctx, o.orgA.ID, "a-c", time.Now().Add(time.Minute)); n != 0 {
		t.Fatalf("future since = %d", n)
	}
	if err := o.s.UpdateActionOutcome(ctx, o.orgB.ID, a.ID, "failed", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign update err = %v", err)
	}
}

func TestOperatorTablesIsolatedByRLS(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	b := o.s.ForOrg(o.orgB.ID)
	for _, table := range []string{"ai_evidence", "ai_usage", "ai_actions"} {
		var other int
		if err := b.QueryRowForTest(ctx, "SELECT count(*) FROM "+table+" WHERE org_id = $1", o.orgA.ID).Scan(&other); err != nil {
			t.Fatal(err)
		}
		if other != 0 {
			t.Errorf("%s: org B read %d rows of org A", table, other)
		}
	}
	// Typed accessors through the scoped view also see only their own rows.
	if n, err := b.UsageToday(ctx, o.orgA.ID, time.Now()); err != nil || n.Total() != 0 {
		t.Fatalf("scoped UsageToday of another org = %+v, %v", n, err)
	}
	if _, err := b.GetAction(ctx, o.orgA.ID, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestOrgAutonomyIsReportWhileAIIsOff(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	org, err := o.s.CreateOrg(ctx, "autonomy")
	if err != nil {
		t.Fatal(err)
	}
	set := func(enabled bool, level string) {
		t.Helper()
		if err := o.s.SetOperatorSettings(ctx, org.ID, store.OperatorSettings{AIEnabled: enabled, Autonomy: level, DailyTokenCap: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, level := range []string{"suggest", "act"} {
		set(true, level)
		if got, err := o.s.OrgAutonomy(ctx, org.ID); err != nil || got != level {
			t.Fatalf("enabled %s: %q %v", level, got, err)
		}
		set(false, level)
		if got, err := o.s.OrgAutonomy(ctx, org.ID); err != nil || got != "report" {
			t.Fatalf("disabled with stored %s: %q %v, want report", level, got, err)
		}
	}
}
