package alerts_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
)

func TestResolveAfterTwoFalseEvaluations(t *testing.T) {
	org := uuid.New()
	host := uuid.New()
	ms := newMemStore(org)
	ms.hosts[org] = []store.Host{{ID: host, OrgID: org, Name: "h1"}}
	n := &recordingNotifier{}
	ev := alerts.NewEvaluator(ms, n, alerts.Config{}, nil)

	c := alerts.Candidate{
		HostID: host, HostName: "h1", Rule: store.RuleHostOffline,
		Severity:  store.SeverityCritical,
		DedupeKey: fmt.Sprintf("%s|%s", store.RuleHostOffline, host),
		Summary:   "h1 offline",
	}
	ctx := context.Background()
	if err := ev.ApplyCandidates(ctx, org, []alerts.Candidate{c}, nil); err != nil {
		t.Fatal(err)
	}
	if ms.firingCount() != 1 {
		t.Fatalf("firing = %d", ms.firingCount())
	}

	// First false: keep firing.
	if err := ev.ApplyCandidates(ctx, org, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ms.firingCount() != 1 {
		t.Fatalf("after 1 false firing = %d", ms.firingCount())
	}
	if ev.FalseCount(org, c.DedupeKey) != 1 {
		t.Fatalf("false count = %d", ev.FalseCount(org, c.DedupeKey))
	}

	// Second false: resolve.
	if err := ev.ApplyCandidates(ctx, org, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ms.firingCount() != 0 {
		t.Fatalf("after 2 false firing = %d", ms.firingCount())
	}
	if ms.openCount() != 0 {
		t.Fatalf("open incidents = %d, want 0", ms.openCount())
	}
}

func TestDedupeIdempotent(t *testing.T) {
	org := uuid.New()
	host := uuid.New()
	ms := newMemStore(org)
	ms.hosts[org] = []store.Host{{ID: host, OrgID: org, Name: "h1"}}
	n := &recordingNotifier{}
	ev := alerts.NewEvaluator(ms, n, alerts.Config{}, nil)
	c := alerts.Candidate{
		HostID: host, HostName: "h1", Rule: store.RuleCPUHigh,
		Severity:  store.SeverityMedium,
		DedupeKey: fmt.Sprintf("%s|%s", store.RuleCPUHigh, host),
		Summary:   "h1 CPU high",
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := ev.ApplyCandidates(ctx, org, []alerts.Candidate{c}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if ms.firingCount() != 1 {
		t.Fatalf("firing alerts = %d, want 1", ms.firingCount())
	}
	if ms.openCount() != 1 {
		t.Fatalf("open incidents = %d, want 1", ms.openCount())
	}
	// First open notifies once; subsequent identical evals should not keep notifying.
	if n.len() != 1 {
		t.Fatalf("notifier calls = %d, want 1", n.len())
	}
}

func TestIncidentGroupingAndEscalation(t *testing.T) {
	org := uuid.New()
	host := uuid.New()
	ms := newMemStore(org)
	ms.hosts[org] = []store.Host{{ID: host, OrgID: org, Name: "hel1-prod"}}
	n := &recordingNotifier{}
	ev := alerts.NewEvaluator(ms, n, alerts.Config{}, nil)
	ctx := context.Background()

	med := alerts.Candidate{
		HostID: host, HostName: "hel1-prod", Rule: store.RuleUnhealthy,
		Container: "web", Severity: store.SeverityMedium,
		DedupeKey: fmt.Sprintf("%s|%s|web", store.RuleUnhealthy, host),
		Summary:   "web unhealthy",
	}
	if err := ev.ApplyCandidates(ctx, org, []alerts.Candidate{med}, nil); err != nil {
		t.Fatal(err)
	}
	high := alerts.Candidate{
		HostID: host, HostName: "hel1-prod", Rule: store.RuleOOMKill,
		Container: "shop", Severity: store.SeverityHigh,
		DedupeKey: fmt.Sprintf("%s|%s|shop", store.RuleOOMKill, host),
		Summary:   "out of memory on hel1-prod",
	}
	if err := ev.ApplyCandidates(ctx, org, []alerts.Candidate{med, high}, nil); err != nil {
		t.Fatal(err)
	}
	if ms.openCount() != 1 {
		t.Fatalf("open = %d, want 1", ms.openCount())
	}
	if ms.firingCount() != 2 {
		t.Fatalf("firing = %d, want 2", ms.firingCount())
	}
	// open + join/escalate
	if n.len() < 2 {
		t.Fatalf("notifier calls = %d, want >= 2 (open + escalate/join)", n.len())
	}
	last := n.calls[n.len()-1]
	if last.inc.Severity != store.SeverityHigh {
		t.Fatalf("incident severity = %s", last.inc.Severity)
	}
}

func TestAllResolvedResolvesIncident(t *testing.T) {
	org := uuid.New()
	host := uuid.New()
	ms := newMemStore(org)
	n := &recordingNotifier{}
	ev := alerts.NewEvaluator(ms, n, alerts.Config{}, nil)
	ctx := context.Background()
	c := alerts.Candidate{
		HostID: host, HostName: "h", Rule: store.RuleMemHigh,
		Severity:  store.SeverityMedium,
		DedupeKey: fmt.Sprintf("%s|%s", store.RuleMemHigh, host),
		Summary:   "h memory high",
	}
	_ = ev.ApplyCandidates(ctx, org, []alerts.Candidate{c}, nil)
	_ = ev.ApplyCandidates(ctx, org, nil, nil)
	_ = ev.ApplyCandidates(ctx, org, nil, nil)
	if ms.openCount() != 0 {
		t.Fatalf("open = %d after resolve", ms.openCount())
	}
	resolved := false
	for _, call := range n.calls {
		if call.inc.State == store.IncidentResolved {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("expected IncidentChanged on resolve")
	}
}

func TestOneOpenIncidentPerHostConcurrent(t *testing.T) {
	org := uuid.New()
	host := uuid.New()
	ms := newMemStore(org)
	ev := alerts.NewEvaluator(ms, alerts.NopNotifier{}, alerts.Config{}, nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := alerts.Candidate{
				HostID: host, HostName: "h", Rule: store.RuleCPUHigh,
				Severity:  store.SeverityMedium,
				DedupeKey: fmt.Sprintf("%s|%s", store.RuleCPUHigh, host),
				Summary:   "cpu",
			}
			// Distinct keys so each goroutine also tries open via a second rule.
			c2 := alerts.Candidate{
				HostID: host, HostName: "h", Rule: store.RuleMemHigh,
				Severity:  store.SeverityMedium,
				DedupeKey: fmt.Sprintf("%s|%s|%d", store.RuleMemHigh, host, i),
				Summary:   "mem",
			}
			_ = ev.ApplyCandidates(ctx, org, []alerts.Candidate{c, c2}, nil)
		}(i)
	}
	wg.Wait()
	if ms.openCount() != 1 {
		t.Fatalf("open incidents = %d, want 1", ms.openCount())
	}
}

func TestMuteSuppressesNotifierButStoresAlert(t *testing.T) {
	org := uuid.New()
	host := uuid.New()
	ms := newMemStore(org)
	ms.mutes = []store.Mute{{
		ID: uuid.New(), OrgID: org, Rule: store.RuleOOMKill,
		Until: time.Now().Add(time.Hour),
	}}
	n := &recordingNotifier{}
	ev := alerts.NewEvaluator(ms, n, alerts.Config{}, nil)
	ctx := context.Background()
	c := alerts.Candidate{
		HostID: host, HostName: "h", Rule: store.RuleOOMKill,
		Container: "shop", Severity: store.SeverityHigh,
		DedupeKey: fmt.Sprintf("%s|%s|shop", store.RuleOOMKill, host),
		Summary:   "out of memory on h",
	}
	if err := ev.ApplyCandidates(ctx, org, []alerts.Candidate{c}, ms.mutes); err != nil {
		t.Fatal(err)
	}
	if ms.firingCount() != 1 {
		t.Fatalf("alert not stored: firing=%d", ms.firingCount())
	}
	if ms.openCount() != 1 {
		t.Fatalf("incident not stored: open=%d", ms.openCount())
	}
	if n.len() != 0 {
		t.Fatalf("notifier called %d times despite mute", n.len())
	}
}
