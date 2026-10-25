package briefing

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

func TestClaimEnqueueCrashReclaims(t *testing.T) {
	orgID := uuid.New()
	ms := newMemStore(store.BriefingOrg{OrgID: orgID, TimeZone: "UTC", BriefingHour: 8})
	enq := &memEnq{}
	clock := &memClock{now: time.Date(2026, 10, 25, 8, 5, 0, 0, time.UTC)}
	ms.clock = clock.Now
	sched := &Scheduler{Store: ms, Enq: enq, Clock: clock, Log: quietLog()}

	// Simulate crash after claim, before enqueue: claim row exists, lease expired.
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	ok, err := ms.BriefingTryClaim(context.Background(), orgID, day)
	if err != nil || !ok {
		t.Fatalf("pre-claim: %v %v", ok, err)
	}
	ms.expireLease(orgID, day)
	if ms.claims() != 1 {
		t.Fatalf("claim missing")
	}

	sched.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("expected reclaim enqueue, got %d", enq.count())
	}
	sched.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("duplicate briefing: %d", enq.count())
	}
}

// skipMarkEnq records enqueue but refuses to let MarkEnqueued run via a wrapper store.
type skipMarkStore struct {
	*memStore
	markCalls atomic.Int32
}

func (s *skipMarkStore) BriefingMarkEnqueued(ctx context.Context, orgID uuid.UUID, localDate time.Time) error {
	s.markCalls.Add(1)
	// Simulate crash: never persist enqueued_at.
	return nil
}

func TestEnqueueSuccessMarkEnqueuedSkippedNoSecondMessage(t *testing.T) {
	orgID := uuid.New()
	inner := newMemStore(store.BriefingOrg{OrgID: orgID, TimeZone: "UTC", BriefingHour: 8})
	clock := &memClock{now: time.Date(2026, 10, 25, 8, 5, 0, 0, time.UTC)}
	inner.clock = clock.Now
	ms := &skipMarkStore{memStore: inner}
	enq := &memEnq{}
	sched := &Scheduler{Store: ms, Enq: enq, Clock: clock, Log: quietLog()}

	sched.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("first enqueue: %d", enq.count())
	}
	if ms.markCalls.Load() != 1 {
		t.Fatalf("MarkEnqueued not attempted")
	}
	// Lease still held (MarkEnqueued skipped) → second tick must not enqueue again.
	sched.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("second tick while lease held: %d", enq.count())
	}
}

func TestConcurrentReclaimExclusive(t *testing.T) {
	orgID := uuid.New()
	ms := newMemStore(store.BriefingOrg{OrgID: orgID, TimeZone: "UTC", BriefingHour: 8})
	clock := &memClock{now: time.Date(2026, 10, 25, 8, 5, 0, 0, time.UTC)}
	ms.clock = clock.Now
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	ok, err := ms.BriefingTryClaim(context.Background(), orgID, day)
	if err != nil || !ok {
		t.Fatal(err)
	}
	ms.expireLease(orgID, day)

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := ms.BriefingReclaimUnenqueued(context.Background(), orgID, day)
			if err != nil {
				t.Errorf("reclaim: %v", err)
				return
			}
			if got {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("exclusive reclaim winners=%d want 1", wins.Load())
	}
}
