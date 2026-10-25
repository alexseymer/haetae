//go:build integration

package deploys_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/agent/deploy"
	"github.com/alexseymer/haetae/internal/agent/deploy/deploytest"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestDeployEventReplayScenarios(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	org, err := st.CreateOrg(ctx, "replay-org")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := st.CreateEnrollmentToken(ctx, org.ID, "replay-host", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	host, err := st.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) {
		return "serial-replay", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	scenarios := []struct {
		name   string
		script func(*deploy.FakeRunner)
		deploy func(id string) *agentv1.Deploy
		want   string
		url    string
	}{
		{
			name:   "build_failure",
			script: deploytest.ScriptInlineBuildFail,
			deploy: func(id string) *agentv1.Deploy {
				return inline(id, "shop-build", "services:\n  web:\n    image: x\n")
			},
			want: store.DeployFailed,
		},
		{
			name: "start_failure_rollback",
			script: func(r *deploy.FakeRunner) {
				deploytest.ScriptInlineStartFailThenRollback(r, "shop-rb")
			},
			deploy: func(id string) *agentv1.Deploy {
				d := inline(id, "shop-rb", "services:\n  web:\n    image: bad\n")
				d.LastHealthy = &agentv1.Version{Kind: &agentv1.Version_Inline{Inline: &agentv1.InlineCompose{
					Content: "services:\n  web:\n    image: good\n",
				}}}
				d.Domain = "shop-rb.example.com"
				d.Service = "web"
				d.Port = 8080
				return d
			},
			want: store.DeployRolledBack,
			url:  "https://shop-rb.example.com",
		},
		{
			name: "healthy",
			script: func(r *deploy.FakeRunner) {
				deploytest.ScriptInlineHappy(r, "shop-ok")
			},
			deploy: func(id string) *agentv1.Deploy {
				d := inline(id, "shop-ok", "services:\n  web:\n    image: nginx\n")
				d.Domain = "shop-ok.example.com"
				d.Service = "web"
				d.Port = 8080
				return d
			},
			want: store.DeployHealthy,
			url:  "https://shop-ok.example.com",
		},
		{
			name:   "rollback_failure",
			script: deploytest.ScriptInlineStartFailRollbackFail,
			deploy: func(id string) *agentv1.Deploy {
				d := inline(id, "shop-rbf", "services:\n  web:\n    image: bad\n")
				d.LastHealthy = &agentv1.Version{Kind: &agentv1.Version_Inline{Inline: &agentv1.InlineCompose{
					Content: "services:\n  web:\n    image: good\n",
				}}}
				return d
			},
			want: store.DeployRollbackFailed,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			proj, err := st.CreateProject(ctx, org.ID, host.ID, uuid.Nil, store.ProjectInput{
				Name: "p-" + sc.name, SourceKind: "inline",
				InlineCompose: "services:\n  web:\n    image: x\n",
			})
			if err != nil {
				t.Fatal(err)
			}
			drow, err := st.CreateDeploy(ctx, proj, store.TriggerManual, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			runner := &deploy.FakeRunner{}
			sc.script(runner)
			emit := &deploytest.RecordEmit{}
			ing := &deploytest.FakeIngress{URL: sc.url}
			m := deploy.New(deploy.Config{
				StateDir:     t.TempDir(),
				Runner:       runner,
				Ingress:      ing,
				Emit:         emit,
				Redact:       deploytest.IdentityRedact,
				HealthWindow: time.Millisecond,
				StartTimeout: time.Second,
				BuildTimeout: time.Minute,
			})
			defer m.Close()

			msg := sc.deploy(drow.ID.String())
			if err := m.Handle(ctx, msg); err != nil {
				t.Fatal(err)
			}
			m.Wait()

			for _, ev := range emit.Events {
				phase := phaseName(ev.GetPhase())
				if phase == "" {
					continue
				}
				if err := st.ApplyDeployEvent(ctx, org.ID, host.ID, drow.ID, store.DeployEvent{
					Phase:   phase,
					Message: ev.GetMessage(),
					Commit:  ev.GetCommit(),
					URL:     ev.GetUrl(),
					Error:   ev.GetError(),
				}); err != nil {
					t.Fatalf("ApplyDeployEvent %s: %v", phase, err)
				}
			}

			got, err := st.GetDeploy(ctx, org.ID, drow.ID)
			if err != nil {
				t.Fatal(err)
			}
			agentLast := phaseName(emit.LastPhase())
			if got.Status != sc.want {
				t.Fatalf("stored status = %q, want %q (agent last %q); events=%v",
					got.Status, sc.want, agentLast, emit.Phases())
			}
			if got.Status != agentLast {
				t.Fatalf("stored %q != agent last phase %q", got.Status, agentLast)
			}
			switch got.Status {
			case store.DeployBuilding, store.DeployStarting, store.DeployFetching, store.DeployChecking, store.DeployQueued:
				t.Fatalf("deploy stuck in intermediate status %q", got.Status)
			}
			if sc.want == store.DeployRolledBack || sc.want == store.DeployHealthy {
				if sc.url != "" && got.URL != sc.url {
					t.Fatalf("url = %q, want %q", got.URL, sc.url)
				}
			}
		})
	}
}

func inline(id, project, content string) *agentv1.Deploy {
	return &agentv1.Deploy{
		DeployId: id,
		Project:  project,
		Source:   &agentv1.Deploy_Inline{Inline: &agentv1.InlineCompose{Content: content}},
	}
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
