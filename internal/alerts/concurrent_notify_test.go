package alerts_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
)

func TestConcurrentEvaluatorsNotifyOnce(t *testing.T) {
	org := uuid.New()
	host := uuid.New()
	ms := newMemStore(org)
	ms.hosts[org] = []store.Host{{ID: host, OrgID: org, Name: "h1"}}
	n := &recordingNotifier{}
	c := alerts.Candidate{
		HostID: host, HostName: "h1", Rule: store.RuleOOMKill,
		Severity:  store.SeverityHigh,
		DedupeKey: fmt.Sprintf("%s|%s|web", store.RuleOOMKill, host),
		Summary:   "web oom",
		Container: "web",
	}
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev := alerts.NewEvaluator(ms, n, alerts.Config{}, nil)
			if err := ev.ApplyCandidates(ctx, org, []alerts.Candidate{c}, nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if ms.firingCount() != 1 {
		t.Fatalf("firing=%d", ms.firingCount())
	}
	// Only the evaluator whose UpsertFiringAlert returned created=true may notify.
	if n.len() != 1 {
		t.Fatalf("notify calls=%d want 1 (concurrent double-notify)", n.len())
	}
}
