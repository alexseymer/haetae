package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// fakeIngress records Route/Unroute calls.
type fakeIngress struct {
	mu     sync.Mutex
	Routes []struct{ Project, Domain, Upstream string }
	URL    string
	Err    error
}

func (f *fakeIngress) Route(_ context.Context, project, domain, upstream string) (string, error) {
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

func (f *fakeIngress) Unroute(context.Context, string) error { return nil }

// recordEmit captures events and logs.
type recordEmit struct {
	mu     sync.Mutex
	Events []*agentv1.DeployEvent
	Logs   []*agentv1.DeployLog
}

func (r *recordEmit) Event(e *agentv1.DeployEvent) {
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

func (r *recordEmit) Log(l *agentv1.DeployLog) {
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

func (r *recordEmit) phases() []agentv1.DeployPhase {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agentv1.DeployPhase, len(r.Events))
	for i, e := range r.Events {
		out[i] = e.GetPhase()
	}
	return out
}

func (r *recordEmit) textBlob() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, e := range r.Events {
		b.WriteString(e.GetMessage())
		b.WriteString(e.GetError())
		b.WriteString(e.GetCommit())
		b.WriteString(e.GetUrl())
	}
	for _, l := range r.Logs {
		b.WriteString(l.GetText())
	}
	return b.String()
}

func identityRedact(_ context.Context, lines []string) ([]string, error) {
	out := make([]string, len(lines))
	copy(out, lines)
	return out, nil
}

func hasVolumeFlag(calls []CmdRecord) bool {
	for _, c := range calls {
		for _, a := range c.Args {
			if a == "-v" || a == "--volumes" {
				return true
			}
		}
	}
	return false
}

func cmdNames(calls []CmdRecord) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Name + " " + strings.Join(c.Args, " ")
	}
	return out
}

func matchCmd(name string, contains ...string) func(Cmd) bool {
	return func(c Cmd) bool {
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

func psHealthyJSON(service, name string) string {
	return `{"Name":"` + name + `","Service":"` + service + `","State":"running","Health":"","Publishers":[{"PublishedPort":8080,"TargetPort":8080,"Protocol":"tcp"}]}`
}

// scriptHappyPath scripts fetch/build/start/ps for an inline deploy.
func scriptInlineHappy(f *FakeRunner, project string) {
	f.Add(
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"built"}},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Stdout: []string{"started"}},
		FakeStep{Match: matchCmd("docker", "ps", "--format"), Stdout: []string{psHealthyJSON("web", project+"-web-1")}, Repeat: -1},
	)
}

func scriptGitHappy(f *FakeRunner, project, commit string) {
	// Write compose after clean so -fdx does not delete it (simulates a tracked file).
	writeCompose := func(c Cmd) {
		_ = os.WriteFile(filepath.Join(c.Dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600)
	}
	f.Add(
		FakeStep{Match: matchCmd("git", "init")},
		FakeStep{Match: matchCmd("git", "remote", "add")},
		FakeStep{Match: matchCmd("git", "fetch")},
		FakeStep{Match: matchCmd("git", "checkout")},
		FakeStep{Match: matchCmd("git", "clean"), After: writeCompose},
		FakeStep{Match: matchCmd("git", "rev-parse"), Stdout: []string{commit}},
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"built"}},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Stdout: []string{"started"}},
		FakeStep{Match: matchCmd("docker", "ps", "--format"), Stdout: []string{psHealthyJSON("web", project+"-web-1")}, Repeat: -1},
	)
}
