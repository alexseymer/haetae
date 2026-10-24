package agent

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func TestOutboxIsBoundedAndSnapshotFirst(t *testing.T) {
	o := NewOutbox(3)
	for i := range 5 {
		o.PutMetrics(&agentv1.MetricsBatch{Host: []*agentv1.HostMetricSample{{TsUnixMs: int64(i)}}})
	}
	o.PutSnapshot(&agentv1.StateSnapshot{TakenUnixMs: 1})
	o.PutSnapshot(&agentv1.StateSnapshot{TakenUnixMs: 2}) // replaces the first

	if got := o.Len(); got != 4 {
		t.Fatalf("Len = %d, want 4 (1 snapshot + 3 batches)", got)
	}
	if got := o.Dropped(); got != 2 {
		t.Fatalf("Dropped = %d, want 2", got)
	}

	ctx := context.Background()
	first, _ := o.Next(ctx)
	if first.GetState().GetTakenUnixMs() != 2 {
		t.Fatalf("first message = %v, want newest snapshot", first)
	}
	for want := int64(2); want <= 4; want++ {
		msg, _ := o.Next(ctx)
		if got := msg.GetMetrics().GetHost()[0].GetTsUnixMs(); got != want {
			t.Fatalf("batch ts = %d, want %d (oldest kept first)", got, want)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := o.Next(ctx); err == nil {
		t.Fatal("Next on empty outbox returned without waiting")
	}
}
