//go:build integration

package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
)

func TestUnhealthySurvivesEvaluatorRestart(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "uh-org")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "h", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "ser", nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContainers(ctx, org.ID, host.ID, []store.Container{{
		ContainerID: "c1", Name: "api", Health: "unhealthy",
	}}, true); err != nil {
		t.Fatal(err)
	}

	n := &recordingNotifier{}
	ev1 := alerts.NewEvaluator(s, n, alerts.Config{}, nil)
	base := time.Now().UTC().Add(-2 * time.Minute)
	ev1.SetNowFunc(func() time.Time { return base })
	// Seed unhealthy-since in the past so the rule fires immediately.
	key := host.ID.String() + "|c1"
	since := base.Add(-90 * time.Second)
	if err := s.UpsertAlertEvalState(ctx, store.AlertEvalState{
		OrgID: org.ID, Kind: store.EvalKindUnhealthy, Key: key, FirstSeen: &since,
	}); err != nil {
		t.Fatal(err)
	}
	ev1.EvaluateOnce(ctx)

	firing, err := s.ListFiringAlerts(ctx, org.ID)
	if err != nil || len(firing) == 0 {
		t.Fatalf("expected firing unhealthy: %v %d", err, len(firing))
	}
	before := n.len()

	// New evaluator over the same DB: still-true condition must neither resolve
	// nor re-notify within two evals.
	ev2 := alerts.NewEvaluator(s, n, alerts.Config{}, nil)
	ev2.SetNowFunc(func() time.Time { return base.Add(15 * time.Second) })
	ev2.EvaluateOnce(ctx)
	ev2.SetNowFunc(func() time.Time { return base.Add(30 * time.Second) })
	ev2.EvaluateOnce(ctx)

	firing2, err := s.ListFiringAlerts(ctx, org.ID)
	if err != nil || len(firing2) == 0 {
		t.Fatalf("false-resolved after restart: %v %d", err, len(firing2))
	}
	if n.len() != before {
		t.Fatalf("re-notified after restart: before=%d after=%d", before, n.len())
	}
}
