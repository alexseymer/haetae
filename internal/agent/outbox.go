package agent

import (
	"context"
	"sync"

	"github.com/alexseymer/haetae/internal/deployphase"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// Outbox holds what the agent still has to send. It is bounded: only the
// newest state snapshot is kept, and metrics batches beyond maxBatches drop
// the oldest. While the control plane is unreachable the agent therefore
// keeps a short window of metrics and never grows without limit.
//
// Deploy events and logs share a separate queue (cap maxDeployMsgs) that
// hard-caps total messages including events. When over the cap: oldest logs
// first, then coalesce duplicate/obsolete phase events, then drop oldest
// events as a last resort.
type Outbox struct {
	maxBatches int
	notify     chan struct{}

	mu       sync.Mutex
	snapshot *agentv1.StateSnapshot
	batches  []*agentv1.MetricsBatch
	logs     []*agentv1.LogBatch
	deploy   []deployMsg
	dropped  uint64
}

type deployMsg struct {
	event  *agentv1.DeployEvent
	log    *agentv1.DeployLog
	branch *agentv1.BranchUpdate
}

// maxLogBatches bounds queued log batches while disconnected. Older batches
// are dropped first and counted; the agent reports the count in LogStats.
const maxLogBatches = 20

// maxDeployMsgs caps queued DeployEvent/DeployLog/BranchUpdate messages.
const maxDeployMsgs = 20000

// NewOutbox returns an outbox keeping at most maxBatches metrics batches.
func NewOutbox(maxBatches int) *Outbox {
	return &Outbox{maxBatches: max(maxBatches, 1), notify: make(chan struct{}, 1)}
}

// PutSnapshot replaces any snapshot not yet sent.
func (o *Outbox) PutSnapshot(s *agentv1.StateSnapshot) {
	o.mu.Lock()
	o.snapshot = s
	o.mu.Unlock()
	o.wake()
}

// PutMetrics queues a batch, dropping the oldest when full.
func (o *Outbox) PutMetrics(b *agentv1.MetricsBatch) {
	o.mu.Lock()
	if len(o.batches) >= o.maxBatches {
		o.batches[0] = nil
		o.batches = o.batches[1:]
		o.dropped++
	}
	o.batches = append(o.batches, b)
	o.mu.Unlock()
	o.wake()
}

// PutLogs queues a log batch, dropping the oldest when full. It returns how
// many events were dropped to make room.
func (o *Outbox) PutLogs(b *agentv1.LogBatch) (droppedEvents int) {
	o.mu.Lock()
	if len(o.logs) >= maxLogBatches {
		droppedEvents = len(o.logs[0].GetEvents())
		o.logs[0] = nil
		o.logs = o.logs[1:]
		o.dropped++
	}
	o.logs = append(o.logs, b)
	o.mu.Unlock()
	o.wake()
	return droppedEvents
}

// Event queues a DeployEvent.
func (o *Outbox) Event(e *agentv1.DeployEvent) {
	o.mu.Lock()
	o.enqueueDeployLocked(deployMsg{event: e})
	o.mu.Unlock()
	o.wake()
}

// Log queues a DeployLog.
func (o *Outbox) Log(l *agentv1.DeployLog) {
	o.mu.Lock()
	o.enqueueDeployLocked(deployMsg{log: l})
	o.mu.Unlock()
	o.wake()
}

// PutBranchUpdate queues a BranchUpdate (same queue as deploy messages).
func (o *Outbox) PutBranchUpdate(u *agentv1.BranchUpdate) {
	o.mu.Lock()
	o.enqueueDeployLocked(deployMsg{branch: u})
	o.mu.Unlock()
	o.wake()
}

func (o *Outbox) enqueueDeployLocked(m deployMsg) {
	// Prefer replacing the latest same deploy+phase event in place so
	// duplicate progress never grows the queue.
	if m.event != nil {
		id := m.event.GetDeployId()
		ph := m.event.GetPhase()
		for i := len(o.deploy) - 1; i >= 0; i-- {
			e := o.deploy[i].event
			if e != nil && e.GetDeployId() == id && e.GetPhase() == ph {
				o.deploy[i] = m
				return
			}
		}
	}
	for len(o.deploy) >= maxDeployMsgs {
		if !o.shedDeployLocked() {
			break
		}
	}
	if len(o.deploy) >= maxDeployMsgs {
		// Still full after shedding: refuse the new message.
		o.dropped++
		return
	}
	o.deploy = append(o.deploy, m)
}

// shedDeployLocked frees one or more slots. Order: oldest logs; coalesce
// duplicate/obsolete phase events; drop oldest events/branches last.
func (o *Outbox) shedDeployLocked() bool {
	if o.dropOldestDeployLogLocked() {
		return true
	}
	before := len(o.deploy)
	o.coalesceDeployEventsLocked()
	if len(o.deploy) < before {
		return true
	}
	if len(o.deploy) == 0 {
		return false
	}
	// Last resort: drop the oldest message (event or branch).
	o.deploy[0] = deployMsg{}
	o.deploy = o.deploy[1:]
	o.dropped++
	return true
}

func (o *Outbox) dropOldestDeployLogLocked() bool {
	for i, m := range o.deploy {
		if m.log != nil {
			copy(o.deploy[i:], o.deploy[i+1:])
			o.deploy[len(o.deploy)-1] = deployMsg{}
			o.deploy = o.deploy[:len(o.deploy)-1]
			o.dropped++
			return true
		}
	}
	return false
}

// coalesceDeployEventsLocked keeps the latest event per (deployID, phase) and
// drops obsolete earlier phases of the same deploy when a later phase exists.
func (o *Outbox) coalesceDeployEventsLocked() {
	type key struct {
		id    string
		phase agentv1.DeployPhase
	}
	latestIdx := make(map[key]int)
	latestRank := make(map[string]int) // deployID → highest Rank seen
	for i, m := range o.deploy {
		if m.event == nil {
			continue
		}
		id := m.event.GetDeployId()
		ph := m.event.GetPhase()
		latestIdx[key{id, ph}] = i
		if r := deployphase.Rank(phaseName(ph)); r > latestRank[id] {
			latestRank[id] = r
		}
	}
	keep := make([]deployMsg, 0, len(o.deploy))
	seen := make(map[key]bool)
	for i, m := range o.deploy {
		if m.event == nil {
			keep = append(keep, m)
			continue
		}
		id := m.event.GetDeployId()
		ph := m.event.GetPhase()
		k := key{id, ph}
		if latestIdx[k] != i {
			o.dropped++
			continue // older duplicate of same deploy+phase
		}
		if r := deployphase.Rank(phaseName(ph)); r < latestRank[id] {
			o.dropped++
			continue // obsolete earlier phase
		}
		if seen[k] {
			o.dropped++
			continue
		}
		seen[k] = true
		keep = append(keep, m)
	}
	o.deploy = keep
}

func phaseName(p agentv1.DeployPhase) string {
	switch p {
	case agentv1.DeployPhase_DEPLOY_PHASE_QUEUED:
		return deployphase.Queued
	case agentv1.DeployPhase_DEPLOY_PHASE_FETCHING:
		return deployphase.Fetching
	case agentv1.DeployPhase_DEPLOY_PHASE_BUILDING:
		return deployphase.Building
	case agentv1.DeployPhase_DEPLOY_PHASE_STARTING:
		return deployphase.Starting
	case agentv1.DeployPhase_DEPLOY_PHASE_CHECKING:
		return deployphase.Checking
	case agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY:
		return deployphase.Healthy
	case agentv1.DeployPhase_DEPLOY_PHASE_FAILED:
		return deployphase.Failed
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLING_BACK:
		return deployphase.RollingBack
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK:
		return deployphase.RolledBack
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLBACK_FAILED:
		return deployphase.RollbackFailed
	default:
		return ""
	}
}

// Dropped reports how many metrics batches / deploy messages were discarded
// because the outbox was full.
func (o *Outbox) Dropped() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dropped
}

// Len reports the number of queued messages.
func (o *Outbox) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(o.batches) + len(o.logs) + len(o.deploy)
	if o.snapshot != nil {
		n++
	}
	return n
}

// DeployQueueLen reports how many deploy/branch messages are queued.
func (o *Outbox) DeployQueueLen() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.deploy)
}

func (o *Outbox) wake() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

// Next blocks until a message is available or ctx ends. Snapshots go first
// so the server's view of the host is current before metrics arrive. Deploy
// messages follow in arrival order, then container log batches, then metrics.
func (o *Outbox) Next(ctx context.Context) (*agentv1.ConnectRequest, error) {
	for {
		if msg := o.take(); msg != nil {
			return msg, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-o.notify:
		}
	}
}

func (o *Outbox) take() *agentv1.ConnectRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s := o.snapshot; s != nil {
		o.snapshot = nil
		return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_State{State: s}}
	}
	if len(o.deploy) > 0 {
		m := o.deploy[0]
		o.deploy[0] = deployMsg{}
		o.deploy = o.deploy[1:]
		switch {
		case m.event != nil:
			return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_DeployEvent{DeployEvent: m.event}}
		case m.log != nil:
			return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_DeployLog{DeployLog: m.log}}
		case m.branch != nil:
			return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_BranchUpdate{BranchUpdate: m.branch}}
		}
	}
	if len(o.logs) > 0 { // problems first: log events can carry errors
		b := o.logs[0]
		o.logs[0] = nil
		o.logs = o.logs[1:]
		return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Logs{Logs: b}}
	}
	if len(o.batches) > 0 {
		b := o.batches[0]
		o.batches[0] = nil
		o.batches = o.batches[1:]
		return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Metrics{Metrics: b}}
	}
	return nil
}
