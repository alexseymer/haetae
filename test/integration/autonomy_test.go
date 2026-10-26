//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/agent"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

func TestAutonomyWelcomeAndPushPolicy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// An org at default is at report.
	if got, err := e.st.OrgAutonomy(ctx, e.org.ID); err != nil || got != "report" {
		t.Fatalf("default autonomy = %q, %v; want report", got, err)
	}
	// The stored level is announced in Welcome.
	if err := setLevel(ctx, e, "suggest"); err != nil {
		t.Fatal(err)
	}

	id, err := agent.Enroll(ctx, "https://"+e.addr, e.token("auto"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := agent.NewPolicy("", nil)
	restarts := make(chan string, 4)
	e.runAgentWith(id, agent.RunOptions{
		Policy:           policy,
		RestartContainer: func(_ context.Context, c string) error { restarts <- c; return nil },
	}, nil)
	e.waitConnected(id.HostID, true, 30*time.Second)
	waitFor(t, 10*time.Second, "welcome autonomy suggest", func() bool { return policy.Effective() == "suggest" })

	hostID := uuid.MustParse(id.HostID)
	restart := func(auth agentv1.Authorization) *agentv1.CommandResult {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		res, err := e.gw.Send(cctx, hostID, &agentv1.Command{
			Authorization: auth,
			Kind:          &agentv1.Command_RestartContainer{RestartContainer: &agentv1.RestartContainer{ContainerId: "0123456789ab"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := restart(agentv1.Authorization_AUTHORIZATION_AUTO); !strings.Contains(res.GetError(), "autonomy level suggest") {
		t.Fatalf("auto at suggest: %v", res)
	}

	// PushPolicy reaches the connected session.
	if err := setLevel(ctx, e, "act"); err != nil {
		t.Fatal(err)
	}
	e.gw.PushPolicy(e.org.ID)
	waitFor(t, 10*time.Second, "pushed autonomy act", func() bool { return policy.Effective() == "act" })
	if res := restart(agentv1.Authorization_AUTHORIZATION_AUTO); res.GetError() != "" {
		t.Fatalf("auto at act: %v", res)
	}
	select {
	case <-restarts:
	case <-time.After(2 * time.Second):
		t.Fatal("restart handler not reached")
	}

	if err := setLevel(ctx, e, "report"); err != nil {
		t.Fatal(err)
	}
	e.gw.PushPolicy(e.org.ID)
	waitFor(t, 10*time.Second, "pushed autonomy report", func() bool { return policy.Effective() == "report" })
}

// setLevel stores the level with the AI enabled (the agent-facing level is
// report whenever the AI is off).
func setLevel(ctx context.Context, e *env, level string) error {
	return e.st.SetOperatorSettings(ctx, e.org.ID, store.OperatorSettings{AIEnabled: true, Autonomy: level, DailyTokenCap: 1000})
}
