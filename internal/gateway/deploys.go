package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

// DeployService is the deploys package surface the gateway needs.
type DeployService interface {
	WatchSet(ctx context.Context, orgID, hostID uuid.UUID) (*agentv1.WatchSet, error)
	OnBranchUpdate(ctx context.Context, orgID, hostID uuid.UUID, projectName, commit string) error
}

const (
	deployEventRate    = 50
	deployLogRate      = 200
	deployBranchPerMin = 6 // BranchUpdate token bucket refill
)

// SetDeploys wires the deploy service used for WatchSet / branch updates.
func (g *Gateway) SetDeploys(svc DeployService) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deploys = svc
}

// RefreshWatch pushes a fresh WatchSet to the host's session, if connected.
func (g *Gateway) RefreshWatch(hostID uuid.UUID) {
	g.mu.Lock()
	s := g.sessions[hostID]
	svc := g.deploys
	g.mu.Unlock()
	if s == nil || svc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, err := svc.WatchSet(ctx, s.orgID, s.hostID)
	if err != nil {
		g.log.Warn("build watch set", "host_id", hostID, "err", err)
		return
	}
	s.setWatch(ws)
}

func (g *Gateway) pushWatch(ctx context.Context, s *session) {
	g.mu.Lock()
	svc := g.deploys
	g.mu.Unlock()
	if svc == nil {
		return
	}
	ws, err := svc.WatchSet(ctx, s.orgID, s.hostID)
	if err != nil {
		g.log.Warn("build watch set", "host_id", s.hostID, "err", err)
		return
	}
	s.setWatch(ws)
}

func phaseName(p agentv1.DeployPhase) string {
	switch p {
	case agentv1.DeployPhase_DEPLOY_PHASE_QUEUED:
		return store.DeployQueued
	case agentv1.DeployPhase_DEPLOY_PHASE_FETCHING:
		return store.DeployFetching
	case agentv1.DeployPhase_DEPLOY_PHASE_BUILDING:
		return store.DeployBuilding
	case agentv1.DeployPhase_DEPLOY_PHASE_STARTING:
		return store.DeployStarting
	case agentv1.DeployPhase_DEPLOY_PHASE_CHECKING:
		return store.DeployChecking
	case agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY:
		return store.DeployHealthy
	case agentv1.DeployPhase_DEPLOY_PHASE_FAILED:
		return store.DeployFailed
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLING_BACK:
		return store.DeployRollingBack
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK:
		return store.DeployRolledBack
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLBACK_FAILED:
		return store.DeployRollbackFailed
	default:
		return ""
	}
}

func (g *Gateway) handleDeployEvent(ctx context.Context, s *session, ev *agentv1.DeployEvent) {
	if ev == nil {
		return
	}
	if !s.eventLim.Allow() {
		s.droppedEvents.Add(1)
		return
	}
	id, err := uuid.Parse(ev.GetDeployId())
	if err != nil {
		return
	}
	phase := phaseName(ev.GetPhase())
	if phase == "" {
		return
	}
	err = g.store.ApplyDeployEvent(ctx, s.orgID, s.hostID, id, store.DeployEvent{
		Phase:   phase,
		Message: clipTo(ev.GetMessage(), store.MaxDeployTextBytes),
		Commit:  clipTo(ev.GetCommit(), store.MaxDeployTextBytes),
		URL:     clipTo(ev.GetUrl(), store.MaxDeployTextBytes),
		Error:   clipTo(ev.GetError(), store.MaxDeployTextBytes),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.warnUnknownDeploy(g, id)
			return
		}
		g.log.Warn("apply deploy event", "host_id", s.hostID, "deploy_id", id, "err", err)
		return
	}
	if phase == store.DeployHealthy {
		g.pushWatch(ctx, s)
	}
}

func (g *Gateway) handleDeployLog(ctx context.Context, s *session, lg *agentv1.DeployLog) {
	if lg == nil {
		return
	}
	if !s.logLim.Allow() {
		s.droppedLogs.Add(1)
		return
	}
	id, err := uuid.Parse(lg.GetDeployId())
	if err != nil {
		return
	}
	err = g.store.AppendDeployLog(ctx, s.orgID, s.hostID, id, store.DeployLogChunk{
		Seq:      int(lg.GetSeq()), //nolint:gosec // G115: seq from agent, clipped by DB
		Stream:   clipTo(lg.GetStream(), 32),
		Text:     clipTo(lg.GetText(), store.MaxLogChunkBytes),
		Withheld: lg.GetWithheld(),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.warnUnknownDeploy(g, id)
			return
		}
		g.log.Warn("append deploy log", "host_id", s.hostID, "deploy_id", id, "err", err)
	}
}

func (g *Gateway) handleBranchUpdate(parent context.Context, s *session, bu *agentv1.BranchUpdate) {
	if bu == nil {
		return
	}
	if !s.branchLim.Allow() {
		s.droppedBranch.Add(1)
		return
	}
	g.mu.Lock()
	svc := g.deploys
	g.mu.Unlock()
	if svc == nil {
		return
	}
	name := clipTo(bu.GetProject(), 64)
	if name == "" {
		return
	}
	commit := clipTo(bu.GetCommit(), 64)
	// OnBranchUpdate may Send a Deploy command and wait for the result. That
	// must not run on the receive loop: the Result arrives on this same loop,
	// so a synchronous Send deadlocks until sendTimeout.
	orgID, hostID := s.orgID, s.hostID
	base := context.WithoutCancel(parent)
	go func() {
		ctx, cancel := context.WithTimeout(base, 60*time.Second)
		defer cancel()
		if err := svc.OnBranchUpdate(ctx, orgID, hostID, name, commit); err != nil {
			g.log.Warn("branch update", "host_id", hostID, "project", name, "err", err)
		}
	}()
}

func (s *session) warnUnknownDeploy(g *Gateway, id uuid.UUID) {
	now := time.Now()
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if now.Sub(s.lastUnknownWarn) < time.Minute {
		return
	}
	s.lastUnknownWarn = now
	g.log.Warn("ignoring deploy report for unknown or other-host deploy", "host_id", s.hostID, "deploy_id", id)
}
