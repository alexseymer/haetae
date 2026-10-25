package deploys

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/secrets"
	"github.com/alexseymer/haetae/internal/store"
)

type fakeStore struct {
	projects map[uuid.UUID]store.Project
	byName   map[string]store.Project
	deploys  []store.Deploy
	running  *store.Deploy
	healthy  *store.Deploy
	failed   []uuid.UUID
	created  int
}

func (f *fakeStore) GetProject(_ context.Context, _, id uuid.UUID) (store.Project, error) {
	p, ok := f.projects[id]
	if !ok {
		return store.Project{}, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) GetProjectByName(_ context.Context, _, _ uuid.UUID, name string) (store.Project, error) {
	p, ok := f.byName[name]
	if !ok {
		return store.Project{}, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) ListProjects(_ context.Context, _, _ uuid.UUID) ([]store.Project, error) {
	var out []store.Project
	for _, p := range f.projects {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) CreateDeploy(_ context.Context, p store.Project, trigger string, rollbackOf, startedBy *uuid.UUID) (store.Deploy, error) {
	f.created++
	d := store.Deploy{
		ID: uuid.New(), OrgID: p.OrgID, HostID: p.HostID, ProjectID: p.ID,
		Status: store.DeployQueued, SourceKind: p.SourceKind, GitURL: p.GitURL,
		GitRef: p.GitRef, ComposePath: p.ComposePath, InlineCompose: p.InlineCompose,
		Trigger: trigger, RollbackOf: rollbackOf, StartedBy: startedBy,
	}
	f.deploys = append(f.deploys, d)
	f.running = &d
	return d, nil
}

func (f *fakeStore) GetDeploy(_ context.Context, _, id uuid.UUID) (store.Deploy, error) {
	for _, d := range f.deploys {
		if d.ID == id {
			return d, nil
		}
	}
	return store.Deploy{}, store.ErrNotFound
}

func (f *fakeStore) ListDeploys(_ context.Context, _, _ uuid.UUID, limit int) ([]store.Deploy, error) {
	out := f.deploys
	if limit > 0 && len(out) > limit {
		// newest first
		out = out[len(out)-limit:]
		rev := make([]store.Deploy, len(out))
		for i := range out {
			rev[len(out)-1-i] = out[i]
		}
		return rev, nil
	}
	rev := make([]store.Deploy, len(out))
	for i := range out {
		rev[len(out)-1-i] = out[i]
	}
	return rev, nil
}

func (f *fakeStore) ApplyDeployEvent(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, store.DeployEvent) error {
	return nil
}

func (f *fakeStore) MarkDeployFailed(_ context.Context, _, _, deployID uuid.UUID, _ string) error {
	f.failed = append(f.failed, deployID)
	f.running = nil
	return nil
}

func (f *fakeStore) LastHealthyDeploy(context.Context, uuid.UUID, uuid.UUID) (store.Deploy, error) {
	if f.healthy == nil {
		return store.Deploy{}, store.ErrNotFound
	}
	return *f.healthy, nil
}

func (f *fakeStore) RunningDeploy(context.Context, uuid.UUID, uuid.UUID) (store.Deploy, error) {
	if f.running == nil {
		return store.Deploy{}, store.ErrNotFound
	}
	return *f.running, nil
}

type fakeSender struct {
	connected bool
	last      *agentv1.Command
	err       error
	result    *agentv1.CommandResult
}

func (f *fakeSender) Connected(uuid.UUID) bool { return f.connected }

func (f *fakeSender) Send(_ context.Context, _ uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error) {
	f.last = cmd
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &agentv1.CommandResult{}, nil
}

func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	b, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStartBusy(t *testing.T) {
	p := store.Project{ID: uuid.New(), OrgID: uuid.New(), HostID: uuid.New(), Name: "shop", SourceKind: "inline", InlineCompose: "x"}
	run := store.Deploy{ID: uuid.New(), Status: store.DeployBuilding}
	svc := &Service{Store: &fakeStore{running: &run}, Sender: &fakeSender{connected: true}}
	_, err := svc.Start(context.Background(), p.OrgID, p.HostID, p, store.TriggerManual, nil, StartOpts{})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}

func TestStartOffline(t *testing.T) {
	p := store.Project{ID: uuid.New(), OrgID: uuid.New(), HostID: uuid.New(), Name: "shop", SourceKind: "inline", InlineCompose: "x"}
	svc := &Service{Store: &fakeStore{}, Sender: &fakeSender{connected: false}}
	_, err := svc.Start(context.Background(), p.OrgID, p.HostID, p, store.TriggerManual, nil, StartOpts{})
	if !errors.Is(err, ErrHostOffline) {
		t.Fatalf("got %v, want ErrHostOffline", err)
	}
}

func TestStartDecryptsTokenAndSetsLastHealthy(t *testing.T) {
	box := testBox(t)
	pid := uuid.New()
	sealed, err := box.Seal([]byte("tok-secret"), pid.String())
	if err != nil {
		t.Fatal(err)
	}
	org, host := uuid.New(), uuid.New()
	p := store.Project{
		ID: pid, OrgID: org, HostID: host, Name: "shop", SourceKind: "git",
		GitURL: "https://github.com/acme/shop", GitRef: "main", ComposePath: "compose.yaml",
		GitTokenSealed: sealed, HasToken: true,
	}
	healthy := store.Deploy{
		ID: uuid.New(), ProjectID: pid, Status: store.DeployHealthy,
		SourceKind: "git", GitURL: p.GitURL, GitRef: "main", ComposePath: "compose.yaml",
		Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	st := &fakeStore{
		projects: map[uuid.UUID]store.Project{pid: p},
		healthy:  &healthy,
	}
	snd := &fakeSender{connected: true}
	svc := &Service{Store: st, Sender: snd, Box: box}
	_, err = svc.Start(context.Background(), org, host, p, store.TriggerManual, nil, StartOpts{})
	if err != nil {
		t.Fatal(err)
	}
	dep := snd.last.GetDeploy()
	if dep.GetGit().GetToken() != "tok-secret" {
		t.Fatalf("token = %q", dep.GetGit().GetToken())
	}
	if dep.GetLastHealthy().GetGit().GetRef() != healthy.Commit {
		t.Fatalf("last_healthy ref = %q", dep.GetLastHealthy().GetGit().GetRef())
	}
	if dep.GetLastHealthy().GetGit().GetToken() != "tok-secret" {
		t.Fatalf("last_healthy token = %q, want project token", dep.GetLastHealthy().GetGit().GetToken())
	}
}

func TestRollbackNeverHealthyRefused(t *testing.T) {
	p := store.Project{ID: uuid.New(), OrgID: uuid.New(), HostID: uuid.New(), Name: "shop", SourceKind: "inline", InlineCompose: "x"}
	failed := store.Deploy{ID: uuid.New(), ProjectID: p.ID, Status: store.DeployFailed, SourceKind: "inline", InlineCompose: "x"}
	st := &fakeStore{deploys: []store.Deploy{failed}}
	svc := &Service{Store: st, Sender: &fakeSender{connected: true}}
	_, err := svc.Rollback(context.Background(), p.OrgID, p.HostID, p, failed.ID, nil)
	if !errors.Is(err, ErrNotHealthy) {
		t.Fatalf("got %v, want ErrNotHealthy", err)
	}
}

func TestWatchSetContents(t *testing.T) {
	box := testBox(t)
	pid := uuid.New()
	sealed, _ := box.Seal([]byte("wtok"), pid.String())
	org, host := uuid.New(), uuid.New()
	auto := store.Project{
		ID: pid, OrgID: org, HostID: host, Name: "auto", SourceKind: "git",
		GitURL: "https://github.com/acme/auto", GitRef: "main", AutoDeploy: true,
		GitTokenSealed: sealed, HasToken: true, ComposePath: "compose.yaml",
	}
	manual := store.Project{
		ID: uuid.New(), OrgID: org, HostID: host, Name: "manual", SourceKind: "git",
		GitURL: "https://github.com/acme/manual", GitRef: "main", AutoDeploy: false,
	}
	st := &fakeStore{
		projects: map[uuid.UUID]store.Project{auto.ID: auto, manual.ID: manual},
		deploys:  []store.Deploy{{ID: uuid.New(), ProjectID: pid, Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	}
	svc := &Service{Store: st, Sender: &fakeSender{connected: true}, Box: box}
	ws, err := svc.WatchSet(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Branches) != 1 || ws.Branches[0].Project != "auto" {
		t.Fatalf("branches = %+v", ws.Branches)
	}
	if ws.Branches[0].Token != "wtok" || ws.Branches[0].LastCommit != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("branch = %+v", ws.Branches[0])
	}
}

func TestOnBranchUpdateIgnored(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	p := store.Project{
		ID: uuid.New(), OrgID: org, HostID: host, Name: "shop", SourceKind: "git",
		GitURL: "https://github.com/acme/shop", GitRef: "main", AutoDeploy: false,
	}
	st := &fakeStore{byName: map[string]store.Project{"shop": p}, projects: map[uuid.UUID]store.Project{p.ID: p}}
	svc := &Service{Store: st, Sender: &fakeSender{connected: true}}
	if err := svc.OnBranchUpdate(context.Background(), org, host, "shop", "c"); err != nil {
		t.Fatal(err)
	}
	if st.created != 0 {
		t.Fatal("non-auto-deploy started a deploy")
	}

	p.AutoDeploy = true
	st.byName["shop"] = p
	st.projects[p.ID] = p
	run := store.Deploy{ID: uuid.New(), Status: store.DeployBuilding}
	st.running = &run
	if err := svc.OnBranchUpdate(context.Background(), org, host, "shop", "c"); err != nil {
		t.Fatal(err)
	}
	if st.created != 0 {
		t.Fatal("running project started another deploy")
	}
}

func TestOnBranchUpdateSameCommitIgnored(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	pid := uuid.New()
	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	p := store.Project{
		ID: pid, OrgID: org, HostID: host, Name: "shop", SourceKind: "git",
		GitURL: "https://github.com/acme/shop", GitRef: "main", AutoDeploy: true,
	}
	st := &fakeStore{
		byName:   map[string]store.Project{"shop": p},
		projects: map[uuid.UUID]store.Project{pid: p},
		deploys: []store.Deploy{{
			ID: uuid.New(), ProjectID: pid, Status: store.DeployHealthy, Commit: commit,
		}},
	}
	svc := &Service{Store: st, Sender: &fakeSender{connected: true}}
	if err := svc.OnBranchUpdate(context.Background(), org, host, "shop", commit); err != nil {
		t.Fatal(err)
	}
	if st.created != 0 {
		t.Fatal("same-commit BranchUpdate started a deploy")
	}
}

func TestOnBranchUpdateCooldown(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	pid := uuid.New()
	p := store.Project{
		ID: pid, OrgID: org, HostID: host, Name: "shop", SourceKind: "git",
		GitURL: "https://github.com/acme/shop", GitRef: "main", AutoDeploy: true,
	}
	st := &fakeStore{
		byName:   map[string]store.Project{"shop": p},
		projects: map[uuid.UUID]store.Project{pid: p},
	}
	now := time.Unix(1_700_000_000, 0)
	svc := &Service{
		Store: st, Sender: &fakeSender{connected: true},
		now: func() time.Time { return now },
	}
	if err := svc.OnBranchUpdate(context.Background(), org, host, "shop", "c1"); err != nil {
		t.Fatal(err)
	}
	if st.created != 1 {
		t.Fatalf("created = %d, want 1", st.created)
	}
	st.running = nil // simulate finished so RunningDeploy is clear
	st.deploys = append(st.deploys, store.Deploy{
		ID: uuid.New(), ProjectID: pid, Status: store.DeployFailed, Commit: "c1",
	})
	if err := svc.OnBranchUpdate(context.Background(), org, host, "shop", "c2"); err != nil {
		t.Fatal(err)
	}
	if st.created != 1 {
		t.Fatal("cooldown did not suppress second push deploy")
	}
	now = now.Add(31 * time.Second)
	if err := svc.OnBranchUpdate(context.Background(), org, host, "shop", "c2"); err != nil {
		t.Fatal(err)
	}
	if st.created != 2 {
		t.Fatalf("after cooldown created = %d, want 2", st.created)
	}
}

func TestStartMarksFailedOnSendError(t *testing.T) {
	p := store.Project{ID: uuid.New(), OrgID: uuid.New(), HostID: uuid.New(), Name: "shop", SourceKind: "inline", InlineCompose: "x"}
	st := &fakeStore{}
	svc := &Service{Store: st, Sender: &fakeSender{connected: true, err: errors.New("boom")}}
	_, err := svc.Start(context.Background(), p.OrgID, p.HostID, p, store.TriggerManual, nil, StartOpts{})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(st.failed) != 1 {
		t.Fatalf("failed marks = %v", st.failed)
	}
}
