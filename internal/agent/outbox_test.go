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

func TestOutboxNeverDropsDeployEvents(t *testing.T) {
	o := NewOutbox(3)
	events := []*agentv1.DeployEvent{
		{DeployId: "e1", Phase: agentv1.DeployPhase_DEPLOY_PHASE_FETCHING},
		{DeployId: "e2", Phase: agentv1.DeployPhase_DEPLOY_PHASE_BUILDING},
		{DeployId: "e3", Phase: agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY},
	}
	o.Event(events[0])
	for i := 0; i < 15000; i++ {
		o.Log(&agentv1.DeployLog{DeployId: "d", Seq: uint32(i), Text: "x"})
	}
	o.Event(events[1])
	for i := 0; i < 15000; i++ {
		o.Log(&agentv1.DeployLog{DeployId: "d", Seq: uint32(15000 + i), Text: "y"})
	}
	o.Event(events[2])

	if n := o.DeployQueueLen(); n > maxDeployMsgs {
		t.Fatalf("queue len %d exceeds cap %d", n, maxDeployMsgs)
	}

	var gotEvents []*agentv1.DeployEvent
	var gotLogs int
	ctx := context.Background()
	for o.DeployQueueLen() > 0 {
		msg, err := o.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case msg.GetDeployEvent() != nil:
			gotEvents = append(gotEvents, msg.GetDeployEvent())
		case msg.GetDeployLog() != nil:
			gotLogs++
		default:
			t.Fatalf("unexpected msg %#v", msg.GetMsg())
		}
	}
	if len(gotEvents) != 3 {
		t.Fatalf("events = %d, want 3 (never dropped); logs kept=%d", len(gotEvents), gotLogs)
	}
	for i, e := range events {
		if gotEvents[i].GetDeployId() != e.GetDeployId() {
			t.Fatalf("event[%d] = %v, want %v", i, gotEvents[i].GetDeployId(), e.GetDeployId())
		}
	}
	if gotLogs > maxDeployMsgs-3 {
		t.Fatalf("too many logs kept: %d", gotLogs)
	}
}
