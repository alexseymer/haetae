//go:build integration

package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func logFixture(t *testing.T) (*store.Store, store.Org, store.Host) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "h", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s", nil })
	if err != nil {
		t.Fatal(err)
	}
	return s, org, h
}

func ev(id string, level int16, count int, at time.Time) store.LogEvent {
	return store.LogEvent{
		ContainerID: "c", TemplateID: id, Level: level, Template: "tmpl " + id, Sample: "sample " + id,
		Count: count, FirstTS: at, LastTS: at,
	}
}

func TestLogTemplatesJudgingRules(t *testing.T) {
	s, org, host := logFixture(t)
	ctx := context.Background()
	now := time.Now()

	// Three windows each for an info, a debug, a warn, an error and an unknown-level template.
	for range 3 {
		err := s.InsertLogEvents(ctx, org.ID, host.ID, []store.LogEvent{
			ev("info", store.LevelInfo, 10, now), ev("debug", store.LevelDebug, 10, now),
			ev("warn", store.LevelWarn, 10, now), ev("error", store.LevelError, 10, now),
			ev("unknown", 0, 10, now),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	ready, err := s.JudgeableTemplates(ctx, org.ID, 20, 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range ready {
		ids = append(ids, r.TemplateID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"debug", "info"}) {
		t.Fatalf("judgeable = %v, want only debug and info", ids)
	}

	// Even if a caller asks, warn, error and unknown can never become green.
	if err := s.SetVerdicts(ctx, org.ID, []string{"info", "warn", "error", "unknown"}, []string{"debug"}); err != nil {
		t.Fatal(err)
	}
	green, err := s.GreenTemplateIDs(ctx, org.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(green, []string{"info"}) {
		t.Fatalf("green = %v, want only info", green)
	}

	// A verdict is not overturned by a later SetVerdicts.
	if err := s.SetVerdicts(ctx, org.ID, []string{"debug"}, nil); err != nil {
		t.Fatal(err)
	}
	if green, _ = s.GreenTemplateIDs(ctx, org.ID, 100); !slices.Equal(green, []string{"info"}) {
		t.Fatalf("a watch verdict was overturned: green = %v", green)
	}
}

func TestLogTemplatesBelowThresholdAreNotJudged(t *testing.T) {
	s, org, host := logFixture(t)
	ctx := context.Background()
	now := time.Now()
	// 500 lines, but in a single window: a burst, not routine traffic.
	if err := s.InsertLogEvents(ctx, org.ID, host.ID, []store.LogEvent{ev("burst", store.LevelInfo, 500, now)}); err != nil {
		t.Fatal(err)
	}
	if ready, _ := s.JudgeableTemplates(ctx, org.ID, 20, 3, 100); len(ready) != 0 {
		t.Fatalf("a one-window burst was judgeable: %+v", ready)
	}
}

func TestLogsAreTenantScoped(t *testing.T) {
	s, orgA, hostA := logFixture(t)
	ctx := context.Background()
	orgB, err := s.CreateOrg(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for range 3 {
		if err := s.InsertLogEvents(ctx, orgA.ID, hostA.ID, []store.LogEvent{ev("a1", store.LevelInfo, 10, now)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetVerdicts(ctx, orgA.ID, []string{"a1"}, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.GreenTemplateIDs(ctx, orgB.ID, 10); len(ids) != 0 {
		t.Fatalf("org B sees org A's green templates: %v", ids)
	}
	if evs, _ := s.ListLogEvents(ctx, orgB.ID, hostA.ID, now.Add(-time.Hour), now.Add(time.Hour), 0, 10); len(evs) != 0 {
		t.Fatalf("org B listed org A's events: %v", evs)
	}
	if _, err := s.GetLogStats(ctx, orgB.ID, hostA.ID); err == nil {
		t.Fatal("org B read org A's log stats")
	}
	if err := s.SetVerdicts(ctx, orgB.ID, []string{"a1"}, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.GreenTemplateIDs(ctx, orgA.ID, 10); !slices.Equal(ids, []string{"a1"}) {
		t.Fatalf("org B's SetVerdicts changed org A: %v", ids)
	}
}

func TestLogStatsAndPrune(t *testing.T) {
	s, org, host := logFixture(t)
	ctx := context.Background()
	want := store.LogStats{LinesIn: 100, LinesSkipped: 95, EventsOut: 3, RedactionFailures: 1, LinesDropped: 2}
	if err := s.SetLogStats(ctx, org.ID, host.ID, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetLogStats(ctx, org.ID, host.ID); err != nil || got != want {
		t.Fatalf("stats = %+v, %v; want %+v", got, err, want)
	}

	old, now := time.Now().Add(-40*24*time.Hour), time.Now()
	if err := s.InsertLogEvents(ctx, org.ID, host.ID, []store.LogEvent{ev("old", store.LevelWarn, 1, old), ev("new", store.LevelWarn, 1, now)}); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneLogs(ctx, now, 30*24*time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.ListLogEvents(ctx, org.ID, host.ID, old.Add(-time.Hour), now.Add(time.Hour), 0, 10)
	if len(evs) != 1 || evs[0].TemplateID != "new" {
		t.Fatalf("after prune: %+v, want only the new event", evs)
	}
}
