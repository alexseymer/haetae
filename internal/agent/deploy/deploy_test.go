package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/deployspec"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func testManager(t *testing.T, runner Runner, emit Emitter, ing Ingress, opts ...func(*Config)) *Manager {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		StateDir:     dir,
		Runner:       runner,
		Ingress:      ing,
		Emit:         emit,
		Redact:       identityRedact,
		HealthWindow: time.Millisecond, // one successful poll is enough
		StartTimeout: time.Second,
		BuildTimeout: time.Minute,
	}
	for _, o := range opts {
		o(&cfg)
	}
	m := New(cfg)
	t.Cleanup(m.Close)
	return m
}

func inlineDeploy(id, project, content string) *agentv1.Deploy {
	return &agentv1.Deploy{
		DeployId: id,
		Project:  project,
		Source:   &agentv1.Deploy_Inline{Inline: &agentv1.InlineCompose{Content: content}},
	}
}

func TestValidationNeverReachesRunner(t *testing.T) {
	runner := &FakeRunner{}
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})

	bads := []struct {
		name string
		d    *agentv1.Deploy
	}{
		{"bad project", inlineDeploy("1", "Shop", "services:\n  web:\n    image: x\n")},
		{"empty source", &agentv1.Deploy{DeployId: "1", Project: "shop"}},
		{"bad git url", &agentv1.Deploy{
			DeployId: "1", Project: "shop",
			Source: &agentv1.Deploy_Git{Git: &agentv1.GitSource{Url: "git@github.com:a/b.git", Ref: "main"}},
		}},
		{"injection project", inlineDeploy("1", "shop;rm", "services:\n  web:\n    image: x\n")},
		{"domain without service", &agentv1.Deploy{
			DeployId: "1", Project: "shop", Domain: "app.example.com",
			Source: &agentv1.Deploy_Inline{Inline: &agentv1.InlineCompose{Content: "services:\n  web:\n    image: x\n"}},
		}},
	}
	// Injection / rejection strings from deployspec tests.
	for _, s := range []string{"", "Shop", "-shop", "shop_", "sh op", "a/b", "../x", "shop;rm", "$(id)"} {
		bads = append(bads, struct {
			name string
			d    *agentv1.Deploy
		}{"project:" + s, inlineDeploy("1", s, "services:\n  web:\n    image: x\n")})
	}
	for _, s := range []string{"http://github.com/a/b", "git@github.com:a/b.git", "file:///tmp/repo",
		"https://user:pw@github.com/a/b", "--upload-pack=evil", "ext::sh -c id"} {
		bads = append(bads, struct {
			name string
			d    *agentv1.Deploy
		}{"url:" + s, &agentv1.Deploy{
			DeployId: "1", Project: "shop",
			Source: &agentv1.Deploy_Git{Git: &agentv1.GitSource{Url: s, Ref: "main"}},
		}})
	}
	for _, s := range []string{"-main", "--upload-pack=x", "a..b", "a@{1}", "$(id)"} {
		bads = append(bads, struct {
			name string
			d    *agentv1.Deploy
		}{"ref:" + s, &agentv1.Deploy{
			DeployId: "1", Project: "shop",
			Source: &agentv1.Deploy_Git{Git: &agentv1.GitSource{Url: "https://github.com/a/b", Ref: s}},
		}})
	}

	ctx := context.Background()
	for _, tc := range bads {
		t.Run(tc.name, func(t *testing.T) {
			if err := m.Handle(ctx, tc.d); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("runner called on validation failure: %v", cmdNames(runner.Calls))
	}
}

func TestValidateBothAndNeitherSource(t *testing.T) {
	if err := validateSource(&agentv1.GitSource{Url: "https://github.com/a/b", Ref: "main"}, &agentv1.InlineCompose{Content: "x"}, false); err == nil {
		t.Fatal("both sources accepted")
	}
	if err := validateSource(nil, nil, false); err == nil {
		t.Fatal("neither source accepted")
	}
	_ = deployspec.ProjectName // keep import used if needed
}

func TestInlineDeployCommandSequence(t *testing.T) {
	runner := &FakeRunner{}
	scriptInlineHappy(runner, "shop")
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	content := "services:\n  web:\n    image: nginx\n"
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", content)); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	phases := emit.phases()
	want := []agentv1.DeployPhase{
		agentv1.DeployPhase_DEPLOY_PHASE_FETCHING,
		agentv1.DeployPhase_DEPLOY_PHASE_BUILDING,
		agentv1.DeployPhase_DEPLOY_PHASE_STARTING,
		agentv1.DeployPhase_DEPLOY_PHASE_CHECKING,
		agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY,
	}
	if len(phases) < len(want) {
		t.Fatalf("phases = %v", phases)
	}
	for i, p := range want {
		if phases[i] != p {
			t.Fatalf("phase[%d] = %v, want %v (all %v)", i, phases[i], p, phases)
		}
	}
	// Exact docker sequence: build then up then ps; never down; never -v.
	var docker []string
	for _, c := range runner.Calls {
		if c.Name != "docker" {
			continue
		}
		docker = append(docker, strings.Join(c.Args, " "))
	}
	if len(docker) < 3 || !strings.Contains(docker[0], "build --pull") || !strings.Contains(docker[1], "up -d") || !strings.Contains(docker[2], "ps --format") {
		t.Fatalf("docker sequence = %v", docker)
	}
	if hasVolumeFlag(runner.Calls) {
		t.Fatal("volume flag present")
	}
	// Inline compose written.
	path := filepath.Join(m.cfg.StateDir, "projects", "shop", "compose.yaml")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != content {
		t.Fatalf("compose file = %q, %v", b, err)
	}
}

func TestGitDeployCommandSequenceAndTokenSecret(t *testing.T) {
	const token = "super-secret-token-xyz"
	const commit = "0123456789abcdef0123456789abcdef01234567"
	runner := &FakeRunner{}
	scriptGitHappy(runner, "shop", commit)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{URL: "https://shop.example.com"}, func(c *Config) {
		c.AllowLocalGit = false
	})
	d := &agentv1.Deploy{
		DeployId: "d1",
		Project:  "shop",
		Domain:   "shop.example.com",
		Service:  "web",
		Port:     8080,
		Source: &agentv1.Deploy_Git{Git: &agentv1.GitSource{
			Url: "https://github.com/acme/shop", Ref: "main", Token: token,
		}},
	}
	if err := m.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	var gitCmds []CmdRecord
	for _, c := range runner.Calls {
		if c.Name == "git" {
			gitCmds = append(gitCmds, c)
		}
		for _, a := range c.Args {
			if strings.Contains(a, token) {
				t.Fatalf("token in argv: %v", c.Args)
			}
		}
	}
	wantGit := []string{"init", "remote add", "fetch", "checkout", "clean", "rev-parse"}
	if len(gitCmds) < len(wantGit) {
		t.Fatalf("git cmds = %v", cmdNames(gitCmds))
	}
	for i, w := range wantGit {
		joined := strings.Join(gitCmds[i].Args, " ")
		if !strings.Contains(joined, w) {
			t.Fatalf("git[%d] = %q, want containing %q", i, joined, w)
		}
	}
	// Hardening flags present on git.
	for _, c := range gitCmds {
		joined := strings.Join(c.Args, " ")
		if !strings.Contains(joined, "protocol.allow=never") || !strings.Contains(joined, "core.hooksPath=/dev/null") {
			t.Fatalf("missing hardening: %s", joined)
		}
	}
	if strings.Contains(emit.textBlob(), token) {
		t.Fatal("token leaked in events/logs")
	}
	var askpassDirs []string
	for _, c := range gitCmds {
		for _, e := range c.Env {
			if strings.HasPrefix(e, "HAETAE_GIT_TOKEN=") {
				t.Fatal("HAETAE_GIT_TOKEN must not appear in child env")
			}
			if after, ok := strings.CutPrefix(e, "GIT_ASKPASS="); ok {
				askpassDirs = append(askpassDirs, filepath.Dir(after))
				if b, err := os.ReadFile(after); err == nil && strings.Contains(string(b), token) {
					t.Fatal("token embedded in askpass script")
				}
			}
			if strings.Contains(e, token) {
				t.Fatalf("token in env entry %q", e)
			}
		}
	}
	if len(askpassDirs) == 0 {
		t.Fatal("expected GIT_ASKPASS on git fetch")
	}
	for _, dir := range askpassDirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("askpass temp dir %s still present after git: %v", dir, err)
		}
	}
	phases := emit.phases()
	if phases[len(phases)-1] != agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY {
		t.Fatalf("phases = %v", phases)
	}
}

func TestSymlinkedComposeRefused(t *testing.T) {
	runner := &FakeRunner{}
	commit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// .git already exists below, so only fetch/checkout/clean/rev-parse run.
	runner.Add(
		FakeStep{Match: matchCmd("git", "fetch")},
		FakeStep{Match: matchCmd("git", "checkout")},
		FakeStep{Match: matchCmd("git", "clean")},
		FakeStep{Match: matchCmd("git", "rev-parse"), Stdout: []string{commit}},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})

	src := filepath.Join(m.cfg.StateDir, "projects", "shop", "src")
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(m.cfg.StateDir, "outside.yaml")
	if err := os.WriteFile(outside, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "compose.yaml")); err != nil {
		t.Fatal(err)
	}

	d := &agentv1.Deploy{
		DeployId: "d1", Project: "shop",
		Source: &agentv1.Deploy_Git{Git: &agentv1.GitSource{Url: "https://github.com/a/b", Ref: "main"}},
	}
	if err := m.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	phases := emit.phases()
	if phases[len(phases)-1] != agentv1.DeployPhase_DEPLOY_PHASE_FAILED {
		t.Fatalf("want FAILED, got %v", phases)
	}
	for _, c := range runner.Calls {
		if c.Name == "docker" {
			t.Fatalf("docker should not run: %v", c.Args)
		}
	}
}

func TestBuildFailureLeavesContainersUntouched(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"step1", "error: boom"}, Err: errors.New("exit 1")},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	phases := emit.phases()
	want := []agentv1.DeployPhase{
		agentv1.DeployPhase_DEPLOY_PHASE_FETCHING,
		agentv1.DeployPhase_DEPLOY_PHASE_BUILDING,
		agentv1.DeployPhase_DEPLOY_PHASE_FAILED,
	}
	if len(phases) != len(want) {
		t.Fatalf("phases = %v", phases)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Fatalf("phases = %v", phases)
		}
	}
	for _, c := range runner.Calls {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, " up ") || strings.HasSuffix(joined, " up") || strings.Contains(joined, "down") {
			t.Fatalf("unexpected up/down: %v", c.Args)
		}
	}
	last := emit.Events[len(emit.Events)-1]
	if last.GetMessage() != "previous version is still running" {
		t.Fatalf("message = %q", last.GetMessage())
	}
}

func TestStartFailureRollsBack(t *testing.T) {
	runner := &FakeRunner{}
	// First deploy: build ok, up fails. Rollback: build, up, ps.
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"ok"}, Repeat: -1},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Err: errors.New("up failed")}, // first up
		FakeStep{Match: matchCmd("docker", "up", "-d"), Stdout: []string{"ok"}},       // rollback up
		FakeStep{Match: matchCmd("docker", "ps", "--format"), Stdout: []string{psHealthyJSON("web", "shop-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	d := inlineDeploy("d1", "shop", "services:\n  web:\n    image: bad\n")
	d.LastHealthy = &agentv1.Version{Kind: &agentv1.Version_Inline{Inline: &agentv1.InlineCompose{Content: "services:\n  web:\n    image: good\n"}}}
	if err := m.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	phases := emit.phases()
	var sawRollback, sawRolledBack bool
	for _, p := range phases {
		if p == agentv1.DeployPhase_DEPLOY_PHASE_ROLLING_BACK {
			sawRollback = true
		}
		if p == agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK {
			sawRolledBack = true
		}
	}
	if !sawRollback || !sawRolledBack {
		t.Fatalf("phases = %v", phases)
	}
	if hasVolumeFlag(runner.Calls) {
		t.Fatal("volume flag")
	}
}

func TestRollbackFailure(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"ok"}, Repeat: -1},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Err: errors.New("up failed"), Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	d := inlineDeploy("d1", "shop", "services:\n  web:\n    image: bad\n")
	d.LastHealthy = &agentv1.Version{Kind: &agentv1.Version_Inline{Inline: &agentv1.InlineCompose{Content: "services:\n  web:\n    image: good\n"}}}
	if err := m.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	phases := emit.phases()
	if phases[len(phases)-1] != agentv1.DeployPhase_DEPLOY_PHASE_ROLLBACK_FAILED {
		t.Fatalf("phases = %v", phases)
	}
}

func TestNoLastHealthyNoDown(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"ok"}},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Err: errors.New("up failed")},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	last := emit.Events[len(emit.Events)-1]
	if last.GetPhase() != agentv1.DeployPhase_DEPLOY_PHASE_FAILED {
		t.Fatalf("phase = %v", last.GetPhase())
	}
	if !strings.Contains(last.GetMessage(), "compose down was not run") {
		t.Fatalf("message = %q", last.GetMessage())
	}
	for _, c := range runner.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "down") {
			t.Fatal("down was called")
		}
	}
}

func TestSecondDeployRefusedWhileRunning(t *testing.T) {
	block := make(chan struct{})
	br := &blockingRunner{
		FakeRunner: &FakeRunner{},
		block:      block,
		entered:    make(chan struct{}),
	}
	br.Add(
		FakeStep{Match: matchCmd("docker", "build"), Stdout: []string{"ok"}},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Stdout: []string{"ok"}},
		FakeStep{Match: matchCmd("docker", "ps"), Stdout: []string{psHealthyJSON("web", "shop-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, br, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-br.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("deploy did not start")
	}
	err := m.Handle(context.Background(), inlineDeploy("d2", "shop", "services:\n  web:\n    image: y\n"))
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second deploy err = %v", err)
	}
	close(block)
	m.Wait()
}

type blockingRunner struct {
	*FakeRunner
	block   chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (b *blockingRunner) Run(ctx context.Context, c Cmd) error {
	b.once.Do(func() { close(b.entered) })
	if b.block != nil {
		select {
		case <-b.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.FakeRunner.Run(ctx, c)
}

func TestDifferentProjectsConcurrent(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build"), Stdout: []string{"ok"}, Repeat: -1},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Stdout: []string{"ok"}, Repeat: -1},
		FakeStep{Match: matchCmd("docker", "ps"), Stdout: []string{psHealthyJSON("web", "a-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("1", "proj-a", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	if err := m.Handle(context.Background(), inlineDeploy("2", "proj-b", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	var healthy int
	for _, e := range emit.Events {
		if e.GetPhase() == agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY {
			healthy++
		}
	}
	if healthy != 2 {
		t.Fatalf("healthy count = %d, phases=%v", healthy, emit.phases())
	}
}

func TestLogChunkingAndSeq(t *testing.T) {
	runner := &FakeRunner{}
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "line")
	}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build"), Stdout: lines},
		FakeStep{Match: matchCmd("docker", "up", "-d")},
		FakeStep{Match: matchCmd("docker", "ps"), Stdout: []string{psHealthyJSON("web", "shop-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if len(emit.Logs) < 2 {
		t.Fatalf("expected batched logs, got %d", len(emit.Logs))
	}
	for i, l := range emit.Logs {
		if l.GetSeq() != uint32(i+1) {
			t.Fatalf("seq[%d] = %d", i, l.GetSeq())
		}
		if l.GetDeployId() != "d1" {
			t.Fatalf("deploy id = %q", l.GetDeployId())
		}
	}
}

func TestPEMBodyNeverEmitted(t *testing.T) {
	runner := &FakeRunner{}
	pem := []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"MIIEowIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF6PZGFw8",
		"-----END RSA PRIVATE KEY-----",
	}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build"), Stdout: pem},
		FakeStep{Match: matchCmd("docker", "up", "-d")},
		FakeStep{Match: matchCmd("docker", "ps"), Stdout: []string{psHealthyJSON("web", "shop-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	blob := emit.textBlob()
	if strings.Contains(blob, "MIIEowIBAAKCAQEA") {
		t.Fatal("PEM body leaked")
	}
}

func TestRedactionFailureWithheld(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build"), Stdout: []string{"secret-line"}},
		FakeStep{Match: matchCmd("docker", "up", "-d")},
		FakeStep{Match: matchCmd("docker", "ps"), Stdout: []string{psHealthyJSON("web", "shop-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{}, func(c *Config) {
		c.Redact = func(context.Context, []string) ([]string, error) {
			return nil, errors.New("boom")
		}
	})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	found := false
	for _, l := range emit.Logs {
		if l.GetWithheld() && l.GetText() == "" {
			found = true
		}
		if strings.Contains(l.GetText(), "secret-line") {
			t.Fatal("unredacted text sent")
		}
	}
	if !found {
		t.Fatalf("no withheld chunk: %+v", emit.Logs)
	}
	for _, e := range emit.Events {
		msg := e.GetMessage()
		if msg != "" && msg != "[withheld]" && strings.Contains(msg, "secret-line") {
			t.Fatal("secret in event")
		}
	}
}

func TestLogCap2MiB(t *testing.T) {
	runner := &FakeRunner{}
	// Emit enough to exceed 2 MiB after redaction.
	big := strings.Repeat("x", 64<<10)
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, big)
	}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build"), Stdout: lines},
		FakeStep{Match: matchCmd("docker", "up", "-d")},
		FakeStep{Match: matchCmd("docker", "ps"), Stdout: []string{psHealthyJSON("web", "shop-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	var truncated bool
	for _, l := range emit.Logs {
		if strings.Contains(l.GetText(), "log truncated") {
			truncated = true
		}
	}
	if !truncated {
		t.Fatalf("expected truncation, logs=%d", len(emit.Logs))
	}
}

func TestCancellationEmitsFailed(t *testing.T) {
	block := make(chan struct{})
	br := &blockingRunner{
		FakeRunner: &FakeRunner{},
		block:      block,
		entered:    make(chan struct{}),
	}
	br.Add(FakeStep{Match: matchCmd("docker", "build"), Stdout: []string{"ok"}, Repeat: -1})
	emit := &recordEmit{}
	m := testManager(t, br, emit, &fakeIngress{})
	if err := m.Handle(context.Background(), inlineDeploy("d1", "shop", "services:\n  web:\n    image: x\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-br.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	m.Close()
	close(block)
	m.Wait()
	phases := emit.phases()
	if len(phases) == 0 || phases[len(phases)-1] != agentv1.DeployPhase_DEPLOY_PHASE_FAILED {
		t.Fatalf("phases = %v", phases)
	}
	last := emit.Events[len(emit.Events)-1]
	if last.GetMessage() != "agent stopped" && last.GetError() != "agent stopped" {
		t.Fatalf("last = %+v", last)
	}
}

func TestRemoveNeverVolumes(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(FakeStep{Match: matchCmd("docker", "compose", "down", "--remove-orphans")})
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	proj := filepath.Join(m.cfg.StateDir, "projects", "shop")
	if err := os.MkdirAll(filepath.Join(proj, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "state.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "compose.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	if hasVolumeFlag(runner.Calls) {
		t.Fatal("volume flag on remove")
	}
	if _, err := os.Stat(filepath.Join(proj, "state.json")); err != nil {
		t.Fatal("state.json should remain")
	}
	if _, err := os.Stat(filepath.Join(proj, "src")); !os.IsNotExist(err) {
		t.Fatal("src should be removed")
	}
}

func TestParseComposePS(t *testing.T) {
	arr, err := parseComposePS([]byte(`[{"Name":"a","Service":"web","State":"running"}]`))
	if err != nil || len(arr) != 1 || arr[0].Service != "web" {
		t.Fatalf("%v %v", arr, err)
	}
	nd, err := parseComposePS([]byte("{\"Name\":\"a\",\"Service\":\"web\",\"State\":\"running\"}\n{\"Name\":\"b\",\"Service\":\"db\",\"State\":\"running\"}\n"))
	if err != nil || len(nd) != 2 {
		t.Fatalf("%v %v", nd, err)
	}
}

func TestFetchFailureDoesNotRollback(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(
		FakeStep{Match: matchCmd("git", "init")},
		FakeStep{Match: matchCmd("git", "remote", "add")},
		FakeStep{Match: matchCmd("git", "fetch"), Err: errors.New("auth failed")},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	d := &agentv1.Deploy{
		DeployId: "d1", Project: "shop",
		Source: &agentv1.Deploy_Git{Git: &agentv1.GitSource{
			Url: "https://github.com/acme/shop", Ref: "main", Token: "tok",
		}},
		LastHealthy: &agentv1.Version{Kind: &agentv1.Version_Inline{Inline: &agentv1.InlineCompose{
			Content: "services:\n  web:\n    image: good\n",
		}}},
	}
	if err := m.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	phases := emit.phases()
	if phases[len(phases)-1] != agentv1.DeployPhase_DEPLOY_PHASE_FAILED {
		t.Fatalf("phases = %v, want FAILED (no rollback)", phases)
	}
	for _, p := range phases {
		if p == agentv1.DeployPhase_DEPLOY_PHASE_ROLLING_BACK {
			t.Fatalf("fetch failure must not roll back: %v", phases)
		}
	}
	last := emit.Events[len(emit.Events)-1]
	if last.GetMessage() != "previous version is still running" {
		t.Fatalf("message = %q", last.GetMessage())
	}
	for _, c := range runner.Calls {
		if c.Name == "docker" {
			t.Fatalf("docker should not run on fetch failure: %v", c.Args)
		}
	}
}

func TestRollbackGitFetchUsesAskpassToken(t *testing.T) {
	const token = "rollback-secret-token"
	const commit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	runner := &FakeRunner{}
	runner.Add(
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"ok"}},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Err: errors.New("up failed")},
	)
	writeCompose := func(c Cmd) {
		_ = os.WriteFile(filepath.Join(c.Dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600)
	}
	runner.Add(
		FakeStep{Match: matchCmd("git", "init")},
		FakeStep{Match: matchCmd("git", "remote", "add")},
		FakeStep{Match: matchCmd("git", "fetch")},
		FakeStep{Match: matchCmd("git", "checkout")},
		FakeStep{Match: matchCmd("git", "clean"), After: writeCompose},
		FakeStep{Match: matchCmd("git", "rev-parse"), Stdout: []string{commit}},
		FakeStep{Match: matchCmd("docker", "build", "--pull"), Stdout: []string{"ok"}},
		FakeStep{Match: matchCmd("docker", "up", "-d"), Stdout: []string{"ok"}},
		FakeStep{Match: matchCmd("docker", "ps", "--format"), Stdout: []string{psHealthyJSON("web", "shop-web-1")}, Repeat: -1},
	)
	emit := &recordEmit{}
	m := testManager(t, runner, emit, &fakeIngress{})
	d := &agentv1.Deploy{
		DeployId: "d1", Project: "shop",
		Source: &agentv1.Deploy_Inline{Inline: &agentv1.InlineCompose{Content: "services:\n  web:\n    image: bad\n"}},
		LastHealthy: &agentv1.Version{Kind: &agentv1.Version_Git{Git: &agentv1.GitSource{
			Url: "https://github.com/acme/shop", Ref: commit, Token: token,
		}}},
	}
	if err := m.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	phases := emit.phases()
	if phases[len(phases)-1] != agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK {
		t.Fatalf("phases = %v", phases)
	}
	var sawRollbackFetch bool
	for _, c := range runner.Calls {
		if c.Name != "git" || !strings.Contains(strings.Join(c.Args, " "), "fetch") {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, token) {
				t.Fatalf("token in argv: %v", c.Args)
			}
		}
		var hasAskpass bool
		for _, e := range c.Env {
			if strings.HasPrefix(e, "HAETAE_GIT_TOKEN=") {
				t.Fatal("HAETAE_GIT_TOKEN in rollback fetch env")
			}
			if strings.HasPrefix(e, "GIT_ASKPASS=") {
				hasAskpass = true
			}
			if strings.Contains(e, token) {
				t.Fatalf("token in env: %q", e)
			}
		}
		if hasAskpass {
			sawRollbackFetch = true
		}
	}
	if !sawRollbackFetch {
		t.Fatal("rollback git fetch missing GIT_ASKPASS")
	}
}
