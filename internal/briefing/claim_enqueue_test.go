package briefing

import (
	"context"
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
	sched := &Scheduler{Store: ms, Enq: enq, Clock: clock, Log: quietLog()}

	// Simulate crash after claim, before enqueue: claim row exists, enqueued_at unset.
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	ok, err := ms.BriefingTryClaim(context.Background(), orgID, day)
	if err != nil || !ok {
		t.Fatalf("pre-claim: %v %v", ok, err)
	}
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
