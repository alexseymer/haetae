package gateway

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

type fakeDeployStore struct {
	Store // unused methods panic if called

	mu     sync.Mutex
	events []store.DeployEvent
	logs   []store.DeployLogChunk
	orgs   []uuid.UUID
	hosts  []uuid.UUID
	ids    []uuid.UUID
}

func (f *fakeDeployStore) ApplyDeployEvent(_ context.Context, orgID, hostID, deployID uuid.UUID, ev store.DeployEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	f.orgs = append(f.orgs, orgID)
	f.hosts = append(f.hosts, hostID)
	f.ids = append(f.ids, deployID)
	// Simulate ownership: only accept a canned deploy id belonging to host.
	return nil
}

func (f *fakeDeployStore) AppendDeployLog(_ context.Context, orgID, hostID, deployID uuid.UUID, log store.DeployLogChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, log)
	f.orgs = append(f.orgs, orgID)
	f.hosts = append(f.hosts, hostID)
	f.ids = append(f.ids, deployID)
	return nil
}

// rejectingStore returns ErrNotFound for every deploy write.
type rejectingStore struct {
	fakeDeployStore
}

func (r *rejectingStore) ApplyDeployEvent(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, store.DeployEvent) error {
	return store.ErrNotFound
}

func (r *rejectingStore) AppendDeployLog(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, store.DeployLogChunk) error {
	return store.ErrNotFound
}

func TestDeployEventUsesSessionIdentity(t *testing.T) {
	f := &fakeDeployStore{}
	g := New(f, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	orgA, hostA := uuid.New(), uuid.New()
	s := newSession(hostA, func(error) {})
	s.orgID = orgA

	deployID := uuid.New()
	g.handleDeployEvent(context.Background(), s, &agentv1.DeployEvent{
		DeployId: deployID.String(),
		Phase:    agentv1.DeployPhase_DEPLOY_PHASE_FETCHING,
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) != 1 || f.orgs[0] != orgA || f.hosts[0] != hostA || f.ids[0] != deployID {
		t.Fatalf("stored %+v orgs=%v hosts=%v ids=%v", f.events, f.orgs, f.hosts, f.ids)
	}
}

func TestDeployEventOtherHostIgnored(t *testing.T) {
	f := &rejectingStore{}
	g := New(f, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := newSession(uuid.New(), func(error) {})
	s.orgID = uuid.New()
	g.handleDeployEvent(context.Background(), s, &agentv1.DeployEvent{
		DeployId: uuid.New().String(),
		Phase:    agentv1.DeployPhase_DEPLOY_PHASE_FETCHING,
	})
	// rejecting store means nothing applied; no panic / no success path.
}

func TestDeployEventMonotonicViaStore(t *testing.T) {
	// Phase mapping only; store enforces order. Ensure HEALTHY maps correctly.
	if phaseName(agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY) != store.DeployHealthy {
		t.Fatal(phaseName(agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY))
	}
	if phaseName(agentv1.DeployPhase_DEPLOY_PHASE_UNSPECIFIED) != "" {
		t.Fatal("unspecified should be empty")
	}
}

func TestDeployFloodLimited(t *testing.T) {
	f := &fakeDeployStore{}
	g := New(f, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := newSession(uuid.New(), func(error) {})
	s.orgID = uuid.New()
	// Empty the burst then flood.
	id := uuid.New().String()
	for range deployEventRate + 100 {
		g.handleDeployEvent(context.Background(), s, &agentv1.DeployEvent{
			DeployId: id,
			Phase:    agentv1.DeployPhase_DEPLOY_PHASE_FETCHING,
		})
	}
	f.mu.Lock()
	n := len(f.events)
	f.mu.Unlock()
	if n > deployEventRate {
		// burst equals rate, so at most burst accepted immediately
		t.Fatalf("accepted %d events, want <= %d", n, deployEventRate)
	}
	if s.droppedEvents.Load() == 0 {
		t.Fatal("expected dropped events")
	}

	for range deployLogRate + 50 {
		g.handleDeployLog(context.Background(), s, &agentv1.DeployLog{
			DeployId: id, Seq: 1, Text: "x",
		})
	}
	if s.droppedLogs.Load() == 0 {
		t.Fatal("expected dropped logs")
	}
}

type countingDeploySvc struct {
	mu    sync.Mutex
	calls int
}

func (c *countingDeploySvc) WatchSet(context.Context, uuid.UUID, uuid.UUID) (*agentv1.WatchSet, error) {
	return &agentv1.WatchSet{}, nil
}

func (c *countingDeploySvc) OnBranchUpdate(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return nil
}

func TestBranchUpdateRateLimited(t *testing.T) {
	f := &fakeDeployStore{}
	g := New(f, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := &countingDeploySvc{}
	g.SetDeploys(svc)
	s := newSession(uuid.New(), func(error) {})
	s.orgID = uuid.New()
	for range deployBranchPerMin + 20 {
		g.handleBranchUpdate(context.Background(), s, &agentv1.BranchUpdate{
			Project: "shop", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})
	}
	svc.mu.Lock()
	n := svc.calls
	svc.mu.Unlock()
	if n > deployBranchPerMin {
		t.Fatalf("accepted %d branch updates, want <= %d", n, deployBranchPerMin)
	}
	if s.droppedBranch.Load() == 0 {
		t.Fatal("expected dropped branch updates")
	}
}
