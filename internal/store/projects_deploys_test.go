//go:build integration

package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

func seedProject(t *testing.T, s *store.Store, orgID, hostID uuid.UUID, name string) store.Project {
	t.Helper()
	p, err := s.CreateProject(context.Background(), orgID, hostID, uuid.Nil, store.ProjectInput{
		Name: name, SourceKind: "git", GitURL: "https://github.com/acme/" + name,
		GitRef: "main", ComposePath: "compose.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectsAndDeploysIsolation(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	projA := seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "shop")
	projB := seedProject(t, o.s, o.orgB.ID, o.hostB.ID, "shop")

	token := []byte("sealed-token-bytes")
	if _, err := o.s.UpdateProject(ctx, o.orgA.ID, projA.ID, store.ProjectInput{
		Name: projA.Name, SourceKind: "git", GitURL: projA.GitURL, GitRef: "main",
		ComposePath: "compose.yaml", SetToken: true, GitTokenSealed: token,
	}); err != nil {
		t.Fatal(err)
	}

	depA, err := o.s.CreateDeploy(ctx, mustGetProject(t, o.s, o.orgA.ID, projA.ID), store.TriggerManual, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.s.AppendDeployLog(ctx, o.orgA.ID, o.hostA.ID, depA.ID, store.DeployLogChunk{Seq: 1, Stream: "stdout", Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	a := o.s.ForOrg(o.orgA.ID)

	// A cannot see B's project/deploy/logs.
	if _, err := a.GetProject(ctx, o.orgB.ID, projB.ID); err == nil {
		t.Fatal("GetProject(B) as A succeeded")
	}
	if ps, _ := a.ListProjects(ctx, o.orgB.ID, o.hostB.ID); len(ps) != 0 {
		t.Fatalf("ListProjects(B) as A = %v", ps)
	}
	if _, err := a.GetDeploy(ctx, o.orgB.ID, depA.ID); err == nil {
		t.Fatal("GetDeploy(B's org, A's deploy) as A succeeded")
	}
	if ds, _ := a.ListDeploys(ctx, o.orgB.ID, projB.ID, 10); len(ds) != 0 {
		t.Fatalf("ListDeploys(B) as A = %v", ds)
	}
	if logs, _ := a.ListDeployLogs(ctx, o.orgB.ID, depA.ID, 0, 10); len(logs) != 0 {
		t.Fatalf("ListDeployLogs across org = %v", logs)
	}

	// Restricted role cannot read git_token_sealed.
	err = a.QueryRowForTest(ctx, `SELECT git_token_sealed FROM projects WHERE id = $1`, projA.ID).Scan(new([]byte))
	if err == nil {
		t.Fatal("scoped role read git_token_sealed")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("want permission denied, got %v", err)
	}
	// But has_git_token is visible.
	var has bool
	if err := a.QueryRowForTest(ctx, `SELECT has_git_token FROM projects WHERE id = $1`, projA.ID).Scan(&has); err != nil || !has {
		t.Fatalf("has_git_token: %v has=%v", err, has)
	}

	// Restricted role cannot write deploy_logs or delete projects.
	for _, stmt := range []string{
		`INSERT INTO deploy_logs (deploy_id, seq, stream, text, withheld, org_id, host_id)
		 SELECT id, 99, 'stdout', 'x', false, org_id, host_id FROM deploys LIMIT 1`,
		`DELETE FROM projects`,
		`DELETE FROM deploys`,
		`DELETE FROM deploy_logs`,
	} {
		if err := a.ExecForTest(ctx, stmt); err == nil {
			t.Errorf("scoped role was allowed: %s", stmt)
		} else if !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("%s: unexpected error %v", stmt, err)
		}
	}

	// Own project is visible; token bytes are not returned through scoped GetProject.
	got, err := a.GetProject(ctx, o.orgA.ID, projA.ID)
	if err != nil || !got.HasToken || len(got.GitTokenSealed) != 0 {
		t.Fatalf("scoped GetProject = %+v, %v", got, err)
	}
	full, err := o.s.GetProject(ctx, o.orgA.ID, projA.ID)
	if err != nil || len(full.GitTokenSealed) == 0 {
		t.Fatalf("server GetProject lost token: %+v %v", full, err)
	}
}

func mustGetProject(t *testing.T, s *store.Store, orgID, id uuid.UUID) store.Project {
	t.Helper()
	p, err := s.GetProject(context.Background(), orgID, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApplyDeployEventOwnershipAndPhases(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	proj := seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "app")
	d, err := o.s.CreateDeploy(ctx, mustGetProject(t, o.s, o.orgA.ID, proj.ID), store.TriggerManual, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong host → not found, no change.
	if err := o.s.ApplyDeployEvent(ctx, o.orgA.ID, o.hostB.ID, d.ID, store.DeployEvent{Phase: store.DeployFetching}); err == nil {
		t.Fatal("ApplyDeployEvent with wrong host succeeded")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not found, got %v", err)
	}
	got, _ := o.s.GetDeploy(ctx, o.orgA.ID, d.ID)
	if got.Status != store.DeployQueued {
		t.Fatalf("status changed after wrong-host event: %s", got.Status)
	}

	steps := []string{store.DeployFetching, store.DeployBuilding, store.DeployStarting, store.DeployChecking, store.DeployHealthy}
	for _, ph := range steps {
		if err := o.s.ApplyDeployEvent(ctx, o.orgA.ID, o.hostA.ID, d.ID, store.DeployEvent{Phase: ph, Commit: "abc", URL: "https://x.test"}); err != nil {
			t.Fatalf("%s: %v", ph, err)
		}
	}
	got, _ = o.s.GetDeploy(ctx, o.orgA.ID, d.ID)
	if got.Status != store.DeployHealthy || got.FinishedAt == nil {
		t.Fatalf("want healthy finished, got %+v", got)
	}
	// Stale: go back to fetching — ignored.
	if err := o.s.ApplyDeployEvent(ctx, o.orgA.ID, o.hostA.ID, d.ID, store.DeployEvent{Phase: store.DeployFetching}); err != nil {
		t.Fatal(err)
	}
	got, _ = o.s.GetDeploy(ctx, o.orgA.ID, d.ID)
	if got.Status != store.DeployHealthy {
		t.Fatalf("stale event changed status to %s", got.Status)
	}

	lh, err := o.s.LastHealthyDeploy(ctx, o.orgA.ID, proj.ID)
	if err != nil || lh.ID != d.ID {
		t.Fatalf("LastHealthyDeploy = %+v %v", lh, err)
	}
}

func TestAppendDeployLogCapsAndIdempotent(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	proj := seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "loggy")
	d, err := o.s.CreateDeploy(ctx, mustGetProject(t, o.s, o.orgA.ID, proj.ID), store.TriggerManual, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	chunk := store.DeployLogChunk{Seq: 1, Stream: "stdout", Text: "one"}
	if err := o.s.AppendDeployLog(ctx, o.orgA.ID, o.hostA.ID, d.ID, chunk); err != nil {
		t.Fatal(err)
	}
	if err := o.s.AppendDeployLog(ctx, o.orgA.ID, o.hostA.ID, d.ID, chunk); err != nil {
		t.Fatal(err)
	}
	logs, err := o.s.ListDeployLogs(ctx, o.orgA.ID, d.ID, 0, 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %v %v", logs, err)
	}
	// Wrong host.
	if err := o.s.AppendDeployLog(ctx, o.orgA.ID, o.hostB.ID, d.ID, store.DeployLogChunk{Seq: 2, Text: "no"}); err == nil {
		t.Fatal("wrong host AppendDeployLog succeeded")
	}
}

func TestExpireAndPruneDeploys(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	proj := seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "stale")
	d, err := o.s.CreateDeploy(ctx, mustGetProject(t, o.s, o.orgA.ID, proj.ID), store.TriggerManual, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Force last_event_at into the past.
	_, err = o.s.Pool().Exec(ctx, `UPDATE deploys SET last_event_at = $2, created_at = $2 WHERE id = $1`,
		d.ID, time.Now().UTC().Add(-20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	n, err := o.s.ExpireStaleDeploys(ctx, time.Now(), 15*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("ExpireStaleDeploys = %d %v", n, err)
	}
	got, _ := o.s.GetDeploy(ctx, o.orgA.ID, d.ID)
	if got.Status != store.DeployFailed || got.Error != "agent stopped reporting" {
		t.Fatalf("expired deploy = %+v", got)
	}
	if err := o.s.PruneDeploys(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunningDeploy(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	proj := seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "run")
	if _, err := o.s.RunningDeploy(ctx, o.orgA.ID, proj.ID); err == nil {
		t.Fatal("expected no running deploy")
	}
	d, err := o.s.CreateDeploy(ctx, mustGetProject(t, o.s, o.orgA.ID, proj.ID), store.TriggerManual, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := o.s.RunningDeploy(ctx, o.orgA.ID, proj.ID)
	if err != nil || run.ID != d.ID {
		t.Fatalf("RunningDeploy = %+v %v", run, err)
	}
}

func TestCreateProjectDuplicateName(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	_ = seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "dup")
	_, err := o.s.CreateProject(ctx, o.orgA.ID, o.hostA.ID, uuid.Nil, store.ProjectInput{
		Name: "dup", SourceKind: "inline", InlineCompose: "services: {}\n",
	})
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("want name taken, got %v", err)
	}
}

func TestUpdateProjectSwitchesSourceKind(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	p := seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "switch")
	got, err := o.s.UpdateProject(ctx, o.orgA.ID, p.ID, store.ProjectInput{
		Name: p.Name, SourceKind: "inline", InlineCompose: "services:\n  web:\n    image: x\n",
		GitURL: "", GitRef: "", ComposePath: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceKind != "inline" || got.InlineCompose == "" || got.GitURL != "" {
		t.Fatalf("after git→inline: %+v", got)
	}
	got, err = o.s.UpdateProject(ctx, o.orgA.ID, p.ID, store.ProjectInput{
		Name: p.Name, SourceKind: "git", GitURL: "https://github.com/acme/switch",
		GitRef: "main", ComposePath: "compose.yaml", InlineCompose: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceKind != "git" || got.GitURL == "" || got.InlineCompose != "" {
		t.Fatalf("after inline→git: %+v", got)
	}
}

func TestCreateDeployOneActiveConcurrency(t *testing.T) {
	o := seedTwoOrgs(t)
	ctx := context.Background()
	proj := seedProject(t, o.s, o.orgA.ID, o.hostA.ID, "busy")
	p := mustGetProject(t, o.s, o.orgA.ID, proj.ID)

	const n = 10
	type result struct {
		d   store.Deploy
		err error
	}
	ch := make(chan result, n)
	for range n {
		go func() {
			d, err := o.s.CreateDeploy(ctx, p, store.TriggerManual, nil, nil)
			ch <- result{d, err}
		}()
	}
	var ok int
	var busy int
	for range n {
		r := <-ch
		if r.err == nil {
			ok++
			continue
		}
		if strings.Contains(r.err.Error(), "already has an active deploy") {
			busy++
			continue
		}
		t.Fatalf("unexpected error: %v", r.err)
	}
	if ok != 1 {
		t.Fatalf("successes = %d, want 1 (busy=%d)", ok, busy)
	}
	if busy != n-1 {
		t.Fatalf("busy = %d, want %d", busy, n-1)
	}
}
