// Package deploytest exports FakeRunner helpers for integration tests outside
// the deploy package (e.g. event-replay through the store).
package deploytest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alexseymer/haetae/internal/agent/deploy"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// FakeIngress records Route/Unroute calls.
type FakeIngress struct {
	mu     sync.Mutex
	Routes []struct{ Project, Domain, Upstream string }
	URL    string
	Err    error
}

// Route implements deploy.Ingress.
func (f *FakeIngress) Route(_ context.Context, project, domain, upstream string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Routes = append(f.Routes, struct{ Project, Domain, Upstream string }{project, domain, upstream})
	if f.Err != nil {
		return "", f.Err
	}
	if f.URL != "" {
		return f.URL, nil
	}
	return "https://" + domain, nil
}

// Unroute implements deploy.Ingress.
func (f *FakeIngress) Unroute(context.Context, string) error { return nil }

// RecordEmit captures events and logs.
type RecordEmit struct {
	mu     sync.Mutex
	Events []*agentv1.DeployEvent
	Logs   []*agentv1.DeployLog
}

// Event implements deploy.Emitter.
func (r *RecordEmit) Event(e *agentv1.DeployEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, &agentv1.DeployEvent{
		DeployId: e.GetDeployId(),
		Phase:    e.GetPhase(),
		Message:  e.GetMessage(),
		Commit:   e.GetCommit(),
		Url:      e.GetUrl(),
		Error:    e.GetError(),
		TsUnixMs: e.GetTsUnixMs(),
	})
}

// Log implements deploy.Emitter.
func (r *RecordEmit) Log(l *agentv1.DeployLog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Logs = append(r.Logs, &agentv1.DeployLog{
		DeployId: l.GetDeployId(),
		Seq:      l.GetSeq(),
		Stream:   l.GetStream(),
		Text:     l.GetText(),
		Withheld: l.GetWithheld(),
	})
}

// Phases returns emitted phase values in order.
func (r *RecordEmit) Phases() []agentv1.DeployPhase {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agentv1.DeployPhase, len(r.Events))
	for i, e := range r.Events {
		out[i] = e.GetPhase()
	}
	return out
}

// LastPhase returns the final emitted phase.
func (r *RecordEmit) LastPhase() agentv1.DeployPhase {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.Events) == 0 {
		return agentv1.DeployPhase_DEPLOY_PHASE_UNSPECIFIED
	}
	return r.Events[len(r.Events)-1].GetPhase()
}

// IdentityRedact copies lines unchanged.
func IdentityRedact(_ context.Context, lines []string) ([]string, error) {
	out := make([]string, len(lines))
	copy(out, lines)
	return out, nil
}

// MatchCmd matches docker/git commands whose args contain all needles.
func MatchCmd(name string, contains ...string) func(deploy.Cmd) bool {
	return func(c deploy.Cmd) bool {
		if c.Name != name {
			return false
		}
		joined := strings.Join(c.Args, " ")
		for _, s := range contains {
			if !strings.Contains(joined, s) {
				return false
			}
		}
		return true
	}
}

// PSHealthyJSON is a compose ps --format json line for a healthy service.
func PSHealthyJSON(service, name string) string {
	return `{"Name":"` + name + `","Service":"` + service + `","State":"running","Health":"","Publishers":[{"PublishedPort":8080,"TargetPort":8080,"Protocol":"tcp"}]}`
}

// ScriptInlineHappy scripts build/up/ps for an inline deploy.
func ScriptInlineHappy(f *deploy.FakeRunner, project string) {
	f.Add(
		deploy.FakeStep{Match: MatchCmd("docker", "build", "--pull"), Stdout: []string{"built"}},
		deploy.FakeStep{Match: MatchCmd("docker", "up", "-d"), Stdout: []string{"started"}},
		deploy.FakeStep{Match: MatchCmd("docker", "ps", "--format"), Stdout: []string{PSHealthyJSON("web", project+"-web-1")}, Repeat: -1},
	)
}

// ScriptInlineBuildFail scripts a failing compose build.
func ScriptInlineBuildFail(f *deploy.FakeRunner) {
	f.Add(
		deploy.FakeStep{Match: MatchCmd("docker", "build", "--pull"), Stdout: []string{"error"}, Err: errBuild},
	)
}

// ScriptInlineStartFailThenRollback scripts start failure then successful rollback.
func ScriptInlineStartFailThenRollback(f *deploy.FakeRunner, project string) {
	f.Add(
		deploy.FakeStep{Match: MatchCmd("docker", "build", "--pull"), Stdout: []string{"ok"}, Repeat: -1},
		deploy.FakeStep{Match: MatchCmd("docker", "up", "-d"), Err: errUp},
		deploy.FakeStep{Match: MatchCmd("docker", "up", "-d"), Stdout: []string{"ok"}},
		deploy.FakeStep{Match: MatchCmd("docker", "ps", "--format"), Stdout: []string{PSHealthyJSON("web", project+"-web-1")}, Repeat: -1},
	)
}

// ScriptInlineStartFailRollbackFail scripts start and rollback both failing.
func ScriptInlineStartFailRollbackFail(f *deploy.FakeRunner) {
	f.Add(
		deploy.FakeStep{Match: MatchCmd("docker", "build", "--pull"), Stdout: []string{"ok"}, Repeat: -1},
		deploy.FakeStep{Match: MatchCmd("docker", "up", "-d"), Err: errUp, Repeat: -1},
	)
}

var (
	errBuild = &simpleErr{"exit 1"}
	errUp    = &simpleErr{"up failed"}
)

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

// WriteComposeAfterClean returns an After hook that writes compose.yaml in Dir.
func WriteComposeAfterClean(content string) func(deploy.Cmd) {
	return func(c deploy.Cmd) {
		_ = os.WriteFile(filepath.Join(c.Dir, "compose.yaml"), []byte(content), 0o600)
	}
}
