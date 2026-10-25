//go:build deploy

package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestDockerInlineHealthyThenRollbackAndRemoveKeepsVolume(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	project := "htest-" + suffix
	volName := project + "_data"

	stateDir := t.TempDir()
	emit := &recordEmit{}
	ing := &fakeIngress{}
	m := New(Config{
		StateDir:     stateDir,
		Runner:       ExecRunner{StateDir: stateDir},
		Ingress:      ing,
		Emit:         emit,
		Redact:       identityRedact,
		HealthWindow: 2 * time.Second,
		StartTimeout: 120 * time.Second,
		BuildTimeout: 5 * time.Minute,
	})
	defer m.Close()
	t.Cleanup(func() { cleanupDockerProject(t, project, volName) })

	v1 := `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "echo v1-data > /data/marker && httpd -f -p 8080 -h /data"]
    volumes:
      - data:/data
volumes:
  data:
`
	d1 := &agentv1.Deploy{
		DeployId: "d1",
		Project:  project,
		Source:   &agentv1.Deploy_Inline{Inline: &agentv1.InlineCompose{Content: v1}},
	}
	if err := m.Handle(context.Background(), d1); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if got := lastPhase(emit); got != agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY {
		t.Fatalf("first deploy phase = %v events=%v", got, emit.phases())
	}

	// Confirm volume marker.
	if out, err := exec.Command("docker", "run", "--rm", "-v", volName+":/data", "busybox:1.37", "cat", "/data/marker").CombinedOutput(); err != nil {
		t.Fatalf("read volume: %v %s", err, out)
	} else if !strings.Contains(string(out), "v1-data") {
		t.Fatalf("marker = %q", out)
	}

	v2 := `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "exit 1"]
    volumes:
      - data:/data
volumes:
  data:
`
	emit2 := &recordEmit{}
	m.cfg.Emit = emit2
	d2 := &agentv1.Deploy{
		DeployId: "d2",
		Project:  project,
		Source:   &agentv1.Deploy_Inline{Inline: &agentv1.InlineCompose{Content: v2}},
		LastHealthy: &agentv1.Version{
			Kind: &agentv1.Version_Inline{Inline: &agentv1.InlineCompose{Content: v1}},
		},
	}
	if err := m.Handle(context.Background(), d2); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if got := lastPhase(emit2); got != agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK {
		t.Fatalf("second deploy phase = %v events=%v", got, emit2.phases())
	}

	// First version's httpd should be running again.
	ps, err := exec.Command("docker", "compose", "-p", project, "ps", "--format", "json").CombinedOutput()
	if err != nil {
		t.Fatalf("ps: %v %s", err, ps)
	}
	if !strings.Contains(string(ps), "running") {
		t.Fatalf("expected running container after rollback: %s", ps)
	}

	if out, err := exec.Command("docker", "run", "--rm", "-v", volName+":/data", "busybox:1.37", "cat", "/data/marker").CombinedOutput(); err != nil {
		t.Fatalf("read volume after rollback: %v %s", err, out)
	} else if !strings.Contains(string(out), "v1-data") {
		t.Fatalf("marker after rollback = %q", out)
	}

	m.cfg.Emit = emit
	if err := m.Remove(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "run", "--rm", "-v", volName+":/data", "busybox:1.37", "cat", "/data/marker").CombinedOutput(); err != nil {
		t.Fatalf("read volume after remove: %v %s", err, out)
	} else if !strings.Contains(string(out), "v1-data") {
		t.Fatalf("marker after remove = %q", out)
	}

	// state.json kept
	if _, err := os.Stat(filepath.Join(stateDir, "projects", project, "state.json")); err != nil {
		t.Fatalf("state.json: %v", err)
	}
}

func lastPhase(e *recordEmit) agentv1.DeployPhase {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.Events) == 0 {
		return agentv1.DeployPhase_DEPLOY_PHASE_UNSPECIFIED
	}
	return e.Events[len(e.Events)-1].GetPhase()
}

func cleanupDockerProject(t *testing.T, project, volName string) {
	t.Helper()
	// Independent timeout: must not depend on a cancelled test context.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := func(args ...string) {
		_ = exec.CommandContext(ctx, "docker", args...).Run()
	}
	run("compose", "-p", project, "down", "--remove-orphans", "-v")
	// Remove any leftover containers with the project label.
	out, _ := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+project).Output()
	for _, id := range strings.Fields(string(out)) {
		run("rm", "-f", id)
	}
	run("volume", "rm", "-f", volName)
	run("network", "rm", project+"_default")
	// Also sweep by name prefix in case labels differ.
	_ = dockerclean.ForceRemove(project)
	if left := dockerclean.List(project); len(left) > 0 {
		t.Errorf("deploy cleanup left resources: %v", left)
	}
}
