package agent

import (
	"context"
	"strconv"
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

func TestOutboxShedsLogsBeforeEvents(t *testing.T) {
	o := NewOutbox(3)
	events := []*agentv1.DeployEvent{
		{DeployId: "e1", Phase: agentv1.DeployPhase_DEPLOY_PHASE_FETCHING},
		{DeployId: "e2", Phase: agentv1.DeployPhase_DEPLOY_PHASE_BUILDING},
		{DeployId: "e3", Phase: agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY},
	}
	o.Event(events[0])
	for i := 0; i < 15000; i++ {
		o.Log(&agentv1.DeployLog{DeployId: "d", Seq: uint32(i), Text: "x"}) //nolint:gosec // G115: test seq
	}
	o.Event(events[1])
	for i := 0; i < 15000; i++ {
		o.Log(&agentv1.DeployLog{DeployId: "d", Seq: uint32(15000 + i), Text: "y"}) //nolint:gosec // G115: test seq
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
		t.Fatalf("events = %d, want 3 (logs shed first); logs kept=%d", len(gotEvents), gotLogs)
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

func TestOutboxHardCapEventsOnly(t *testing.T) {
	o := NewOutbox(3)
	const extra = 500
	for i := 0; i < maxDeployMsgs+extra; i++ {
		o.Event(&agentv1.DeployEvent{
			DeployId: "flood",
			Phase:    agentv1.DeployPhase_DEPLOY_PHASE_FETCHING,
			Message:  "m",
			TsUnixMs: int64(i),
		})
	}
	// Same deploy+phase replaces in place — queue stays at 1.
	if n := o.DeployQueueLen(); n != 1 {
		t.Fatalf("queue len %d, want 1 (in-place coalesce of duplicate phase)", n)
	}
	// Distinct deploys fill to the hard cap; further events drop.
	for i := 0; i < maxDeployMsgs+extra; i++ {
		o.Event(&agentv1.DeployEvent{
			DeployId: "d-" + strconv.Itoa(i),
			Phase:    agentv1.DeployPhase_DEPLOY_PHASE_BUILDING,
			TsUnixMs: int64(i),
		})
	}
	if n := o.DeployQueueLen(); n > maxDeployMsgs {
		t.Fatalf("queue len %d exceeds hard cap %d", n, maxDeployMsgs)
	}
	if o.Dropped() == 0 {
		t.Fatal("expected drops when flooding distinct events past the cap")
	}
}

func TestOutboxHardCapMixedFlood(t *testing.T) {
	o := NewOutbox(3)
	for i := 0; i < maxDeployMsgs+200; i++ {
		o.Log(&agentv1.DeployLog{DeployId: "a", Seq: uint32(i % 100000), Text: "log"}) //nolint:gosec
		o.Event(&agentv1.DeployEvent{
			DeployId: "d-" + strconv.Itoa(i),
			Phase:    agentv1.DeployPhase_DEPLOY_PHASE_BUILDING,
			TsUnixMs: int64(i),
		})
		if i%7 == 0 {
			o.PutBranchUpdate(&agentv1.BranchUpdate{Project: "p", Commit: strconv.Itoa(i)})
		}
	}
	if n := o.DeployQueueLen(); n > maxDeployMsgs {
		t.Fatalf("queue len %d exceeds hard cap %d", n, maxDeployMsgs)
	}
	if o.Dropped() == 0 {
		t.Fatal("expected drops under mixed flood")
	}
}

func TestOutboxSbomBounded(t *testing.T) {
	o := NewOutbox(3)
	for i := 0; i < maxSbomReports+10; i++ {
		o.PutSbom(&agentv1.SbomReport{ImageId: "img-" + strconv.Itoa(i), Status: "ok"})
	}
	if n := o.Len(); n > maxSbomReports {
		t.Fatalf("Len = %d, want at most %d SbomReports", n, maxSbomReports)
	}
	if o.Dropped() == 0 {
		t.Fatal("expected drops when flooding SbomReports past the cap")
	}
	// Oldest were shed; newest should still be present.
	ctx := context.Background()
	var ids []string
	for o.Len() > 0 {
		msg, err := o.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.GetSbomReport().GetImageId())
	}
	if len(ids) != maxSbomReports {
		t.Fatalf("drained %d reports, want %d", len(ids), maxSbomReports)
	}
	if ids[0] != "img-10" || ids[len(ids)-1] != "img-"+strconv.Itoa(maxSbomReports+9) {
		t.Fatalf("kept range %q..%q, want img-10..img-%d", ids[0], ids[len(ids)-1], maxSbomReports+9)
	}
}

func TestOutboxSbomDoesNotDropDeployOrTerminal(t *testing.T) {
	o := NewOutbox(3)
	terminal := &agentv1.DeployEvent{
		DeployId: "keep-healthy",
		Phase:    agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY,
		TsUnixMs: 1,
	}
	progress := &agentv1.DeployEvent{
		DeployId: "keep-building",
		Phase:    agentv1.DeployPhase_DEPLOY_PHASE_BUILDING,
		TsUnixMs: 2,
	}
	o.Event(terminal)
	o.Event(progress)
	for i := 0; i < maxSbomReports+20; i++ {
		o.PutSbom(&agentv1.SbomReport{ImageId: "flood-" + strconv.Itoa(i), Status: "ok"})
	}
	if o.DeployQueueLen() != 2 {
		t.Fatalf("deploy queue = %d, want 2 (Sbom drops must not touch deploy)", o.DeployQueueLen())
	}
	if n := o.Len(); n != 2+maxSbomReports {
		t.Fatalf("Len = %d, want %d (2 deploy + capped sboms)", n, 2+maxSbomReports)
	}

	ctx := context.Background()
	var sawTerminal, sawBuilding bool
	for o.Len() > 0 {
		msg, err := o.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev := msg.GetDeployEvent(); ev != nil {
			if ev.GetDeployId() == "keep-healthy" {
				sawTerminal = true
			}
			if ev.GetDeployId() == "keep-building" {
				sawBuilding = true
			}
		}
	}
	if !sawTerminal || !sawBuilding {
		t.Fatalf("deploy events lost to Sbom flood: terminal=%v building=%v", sawTerminal, sawBuilding)
	}
}

func TestOutboxLastResortKeepsTerminalWhileNonTerminalExists(t *testing.T) {
	o := NewOutbox(3)
	terminal := &agentv1.DeployEvent{
		DeployId: "keep-healthy",
		Phase:    agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY,
		TsUnixMs: 1,
	}
	o.Event(terminal)
	// Fill to the hard cap with distinct non-terminal events (forces last-resort shed).
	for i := 0; i < maxDeployMsgs+50; i++ {
		o.Event(&agentv1.DeployEvent{
			DeployId: "n-" + strconv.Itoa(i),
			Phase:    agentv1.DeployPhase_DEPLOY_PHASE_BUILDING,
			TsUnixMs: int64(i + 2),
		})
	}
	if n := o.DeployQueueLen(); n > maxDeployMsgs {
		t.Fatalf("queue len %d exceeds hard cap %d", n, maxDeployMsgs)
	}

	var sawTerminal, sawNonTerminal bool
	ctx := context.Background()
	for o.DeployQueueLen() > 0 {
		msg, err := o.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ev := msg.GetDeployEvent()
		if ev == nil {
			continue
		}
		if ev.GetDeployId() == "keep-healthy" && ev.GetPhase() == agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY {
			sawTerminal = true
		}
		if ev.GetPhase() == agentv1.DeployPhase_DEPLOY_PHASE_BUILDING {
			sawNonTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("last-resort shed dropped terminal healthy while non-terminals were present")
	}
	if !sawNonTerminal {
		t.Fatal("expected some non-terminal events to remain under the cap")
	}
}

func TestOutboxLastResortDropsOldestWhenOnlyTerminals(t *testing.T) {
	o := NewOutbox(3)
	phases := []agentv1.DeployPhase{
		agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY,
		agentv1.DeployPhase_DEPLOY_PHASE_FAILED,
		agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK,
		agentv1.DeployPhase_DEPLOY_PHASE_ROLLBACK_FAILED,
	}
	for i := 0; i < maxDeployMsgs+10; i++ {
		o.Event(&agentv1.DeployEvent{
			DeployId: "t-" + strconv.Itoa(i),
			Phase:    phases[i%len(phases)],
			TsUnixMs: int64(i),
		})
	}
	if n := o.DeployQueueLen(); n != maxDeployMsgs {
		t.Fatalf("queue len %d, want hard cap %d", n, maxDeployMsgs)
	}
	// Oldest terminals were shed; newest ts should be present.
	ctx := context.Background()
	var minTS, maxTS int64 = -1, -1
	for o.DeployQueueLen() > 0 {
		msg, err := o.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ts := msg.GetDeployEvent().GetTsUnixMs()
		if minTS < 0 || ts < minTS {
			minTS = ts
		}
		if ts > maxTS {
			maxTS = ts
		}
	}
	if minTS != 10 { // first 10 (0..9) dropped to make room for +10 over cap
		t.Fatalf("oldest kept ts = %d, want 10 (oldest terminals dropped first)", minTS)
	}
	if maxTS != int64(maxDeployMsgs+9) {
		t.Fatalf("newest ts = %d, want %d", maxTS, maxDeployMsgs+9)
	}
}
