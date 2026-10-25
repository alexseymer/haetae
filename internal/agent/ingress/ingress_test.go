package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner records calls and returns scripted step responses.
type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	steps []fakeStep
}

type fakeCall struct {
	Name string
	Args []string
}

type fakeStep struct {
	match  func(name string, args []string) bool
	stdout []string
	err    error
	repeat int // 0 = once, -1 = forever
}

func (f *fakeRunner) add(steps ...fakeStep) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, steps...)
}

func (f *fakeRunner) Run(ctx context.Context, name string, args []string, onLine func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Name: name, Args: append([]string(nil), args...)})
	step, ok := f.take(name, args)
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("fakeRunner: unexpected %s %v", name, args)
	}
	for _, line := range step.stdout {
		if onLine != nil {
			onLine(line)
		}
	}
	if step.err != nil {
		return step.err
	}
	return ctx.Err()
}

func (f *fakeRunner) take(name string, args []string) (fakeStep, bool) {
	for i, s := range f.steps {
		if s.match != nil && !s.match(name, args) {
			continue
		}
		out := s
		if s.repeat < 0 {
			return out, true
		}
		if s.repeat > 1 {
			f.steps[i].repeat--
			return out, true
		}
		f.steps = append(f.steps[:i], f.steps[i+1:]...)
		return out, true
	}
	return fakeStep{}, false
}

func (f *fakeRunner) callArgs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = append([]string{c.Name}, c.Args...)
	}
	return out
}

func matchArgs(name string, args ...string) func(string, []string) bool {
	return func(n string, a []string) bool {
		if n != name || len(a) != len(args) {
			return false
		}
		for i := range args {
			if a[i] != args[i] {
				return false
			}
		}
		return true
	}
}

func matchHas(name, needle string) func(string, []string) bool {
	return func(n string, a []string) bool {
		if n != name {
			return false
		}
		for _, x := range a {
			if x == needle {
				return true
			}
		}
		return false
	}
}

func testConfig(t *testing.T, r Runner) Config {
	t.Helper()
	return Config{
		StateDir:      t.TempDir(),
		HTTPPort:      8080,
		HTTPSPort:     8443,
		TLSInternal:   true,
		ContainerName: "haetae-caddy-test",
		VolumePrefix:  "haetae-caddy-test",
		Runner:        r,
		Now:           func() time.Time { return time.Unix(0, 0) },
	}
}

func TestNew_PortsValidation(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}
	_, err := New(Config{StateDir: dir, HTTPPort: 0, HTTPSPort: 70000, Runner: r})
	if err == nil {
		t.Fatal("expected HTTPSPort error")
	}
	_, err = New(Config{StateDir: dir, HTTPPort: -1, HTTPSPort: 443, Runner: r})
	if err == nil {
		t.Fatal("expected HTTPPort error")
	}
	ing, err := New(Config{StateDir: dir, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()
	if ing.httpPort != 80 || ing.httpsPort != 443 {
		t.Fatalf("defaults: got %d/%d", ing.httpPort, ing.httpsPort)
	}
	st, err := os.Stat(filepath.Join(dir, "ingress"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o777 != 0o700 {
		t.Fatalf("ingress dir mode = %o", st.Mode().Perm())
	}
}

func TestInjectionRejected(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		domain   string
		upstream string
	}{
		{"domain newline", "app", "app.example.com\n", "web:80"},
		{"domain braces", "app", "app{.example.com", "web:80"},
		{"domain space", "app", "app example.com", "web:80"},
		{"domain star", "app", "*.example.com", "web:80"},
		{"domain env", "app", "{$ENV}.example.com", "web:80"},
		{"domain import", "app", "import.example.com;import", "web:80"},
		{"upstream newline", "app", "app.example.com", "web:80\n"},
		{"upstream braces", "app", "app.example.com", "web{:80"},
		{"upstream space", "app", "app.example.com", "we b:80"},
		{"upstream star", "app", "app.example.com", "*:80"},
		{"upstream env", "app", "app.example.com", "{$ENV}:80"},
		{"upstream import", "app", "app.example.com", "import:80;"},
		{"upstream semicolon", "app", "app.example.com", "web:80;"},
		{"project newline", "ap\np", "app.example.com", "web:80"},
		{"project braces", "ap{p", "app.example.com", "web:80"},
		{"project space", "ap p", "app.example.com", "web:80"},
		{"project star", "ap*p", "app.example.com", "web:80"},
		{"empty domain", "app", "", "web:80"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{}
			cfg := testConfig(t, r)
			ing, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer ing.Close()

			_, err = ing.Route(context.Background(), tc.project, tc.domain, tc.upstream)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if len(r.callArgs()) != 0 {
				t.Fatalf("unexpected docker calls: %v", r.callArgs())
			}
			if _, err := os.Stat(filepath.Join(cfg.StateDir, "ingress", "routes.json")); !os.IsNotExist(err) {
				t.Fatalf("routes.json should not exist, err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(cfg.StateDir, "ingress", "Caddyfile")); !os.IsNotExist(err) {
				t.Fatalf("Caddyfile should not exist, err=%v", err)
			}
		})
	}
}

func TestDomainCollision(t *testing.T) {
	r := &fakeRunner{}
	r.add(
		fakeStep{match: matchHas("docker", "inspect"), err: errors.New("no such object")},
		fakeStep{match: matchHas("docker", "run")},
		fakeStep{match: matchHas("docker", "inspect"), stdout: []string{"bridge "}},
		fakeStep{match: matchHas("docker", "connect")},
		fakeStep{match: matchHas("docker", "reload")},
	)
	ing, err := New(testConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()

	if _, err := ing.Route(context.Background(), "one", "app.example.com", "web:80"); err != nil {
		t.Fatal(err)
	}
	_, err = ing.Route(context.Background(), "two", "app.example.com", "other:80")
	if err == nil || !strings.Contains(err.Error(), "one") {
		t.Fatalf("expected collision naming project one, got %v", err)
	}
}

func scriptFirstRoute(r *fakeRunner, caddy, upstream string) {
	r.add(
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{.State.Running}}", caddy), err: errors.New("No such object")},
		fakeStep{match: matchHas("docker", "run")},
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", upstream), stdout: []string{"proj_net "}},
		fakeStep{match: matchArgs("docker", "network", "connect", "proj_net", caddy)},
		fakeStep{match: matchArgs("docker", "exec", caddy, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--force")},
	)
}

func TestRouteFirstCommandSequence(t *testing.T) {
	r := &fakeRunner{}
	cfg := testConfig(t, r)
	scriptFirstRoute(r, cfg.ContainerName, "web")
	ing, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()

	url, err := ing.Route(context.Background(), "app", "app.example.com", "web:80")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://app.example.com:8443" {
		t.Fatalf("url = %q", url)
	}

	calls := r.callArgs()
	wantSeq := []string{"inspect", "run", "inspect", "connect", "reload"}
	if len(calls) != len(wantSeq) {
		t.Fatalf("calls = %v", calls)
	}
	for i, w := range wantSeq {
		joined := strings.Join(calls[i], " ")
		if !strings.Contains(joined, w) {
			t.Fatalf("call %d = %v, want containing %q", i, calls[i], w)
		}
	}
	// First inspect missing → run (not start).
	if calls[1][1] != "run" {
		t.Fatalf("expected run, got %v", calls[1])
	}

	// routes.json mode and content
	info, err := os.Stat(filepath.Join(cfg.StateDir, "ingress", "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o777 != 0o600 {
		t.Fatalf("routes.json mode = %o", info.Mode().Perm())
	}
	cf, err := os.ReadFile(filepath.Join(cfg.StateDir, "ingress", "Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cf), "tls internal") {
		t.Fatalf("Caddyfile missing tls internal:\n%s", cf)
	}
}

func TestRouteSecondNoRun(t *testing.T) {
	r := &fakeRunner{}
	cfg := testConfig(t, r)
	scriptFirstRoute(r, cfg.ContainerName, "web")
	// Second route: inspect running, network for other, reload — no run.
	r.add(
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{.State.Running}}", cfg.ContainerName), stdout: []string{"true"}},
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", "api"), stdout: []string{"proj_net "}},
		fakeStep{match: matchArgs("docker", "network", "connect", "proj_net", cfg.ContainerName), err: errors.New("already exists")},
		fakeStep{match: matchArgs("docker", "exec", cfg.ContainerName, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--force")},
	)
	ing, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()

	if _, err := ing.Route(context.Background(), "app", "app.example.com", "web:80"); err != nil {
		t.Fatal(err)
	}
	n := len(r.callArgs())
	if _, err := ing.Route(context.Background(), "api", "api.example.com", "api:8080"); err != nil {
		t.Fatal(err)
	}
	calls := r.callArgs()[n:]
	for _, c := range calls {
		if len(c) > 1 && c[1] == "run" {
			t.Fatalf("second route must not run: %v", calls)
		}
	}
	joined := fmt.Sprintf("%v", calls)
	if !strings.Contains(joined, "reload") {
		t.Fatalf("expected reload: %v", calls)
	}
}

func TestUnroute(t *testing.T) {
	r := &fakeRunner{}
	cfg := testConfig(t, r)
	scriptFirstRoute(r, cfg.ContainerName, "web")
	r.add(
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{.State.Running}}", cfg.ContainerName), stdout: []string{"true"}},
		fakeStep{match: matchArgs("docker", "exec", cfg.ContainerName, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--force")},
	)
	ing, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()

	if _, err := ing.Route(context.Background(), "app", "app.example.com", "web:80"); err != nil {
		t.Fatal(err)
	}
	if err := ing.Unroute(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.StateDir, "ingress", "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var routes map[string]route
	if err := json.Unmarshal(data, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %v", routes)
	}
	cf, err := os.ReadFile(filepath.Join(cfg.StateDir, "ingress", "Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cf), "app.example.com") {
		t.Fatalf("Caddyfile still has site: %s", cf)
	}
}

func TestContainerStoppedStarts(t *testing.T) {
	r := &fakeRunner{}
	cfg := testConfig(t, r)
	r.add(
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{.State.Running}}", cfg.ContainerName), stdout: []string{"false"}},
		fakeStep{match: matchArgs("docker", "start", cfg.ContainerName)},
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", "web"), stdout: []string{"bridge "}},
		fakeStep{match: matchArgs("docker", "network", "connect", "bridge", cfg.ContainerName)},
		fakeStep{match: matchArgs("docker", "exec", cfg.ContainerName, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--force")},
	)
	ing, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()

	if _, err := ing.Route(context.Background(), "app", "app.example.com", "web:80"); err != nil {
		t.Fatal(err)
	}
	calls := r.callArgs()
	if len(calls) < 2 || calls[1][1] != "start" {
		t.Fatalf("expected start after stopped inspect, got %v", calls)
	}
	for _, c := range calls {
		if len(c) > 1 && c[1] == "run" {
			t.Fatalf("must not run when container exists: %v", calls)
		}
	}
}

func TestRoutesAtomicAndRestore(t *testing.T) {
	r1 := &fakeRunner{}
	cfg := testConfig(t, r1)
	scriptFirstRoute(r1, cfg.ContainerName, "web")
	ing1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ing1.Route(context.Background(), "app", "app.example.com", "web:80"); err != nil {
		t.Fatal(err)
	}
	ing1.Close()

	// "Restart": new Ingress on same dir, no docker until Restore.
	r2 := &fakeRunner{}
	cfg.Runner = r2
	ing2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ing2.Close()
	if len(r2.callArgs()) != 0 {
		t.Fatalf("New must not run docker: %v", r2.callArgs())
	}
	if len(ing2.routes) != 1 || ing2.routes["app"].Domain != "app.example.com" {
		t.Fatalf("loaded routes = %v", ing2.routes)
	}

	r2.add(
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{.State.Running}}", cfg.ContainerName), stdout: []string{"true"}},
		fakeStep{match: matchArgs("docker", "inspect", "-f", "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", "web"), stdout: []string{"proj_net "}},
		fakeStep{match: matchArgs("docker", "network", "connect", "proj_net", cfg.ContainerName), err: errors.New("endpoint already exists")},
		fakeStep{match: matchArgs("docker", "exec", cfg.ContainerName, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--force")},
	)
	if err := ing2.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r2.callArgs()) == 0 {
		t.Fatal("Restore should drive docker")
	}

	// Atomic write: no leftover .tmp
	entries, err := os.ReadDir(filepath.Join(cfg.StateDir, "ingress"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp: %s", e.Name())
		}
	}
}

func TestPublicURLDefault443(t *testing.T) {
	r := &fakeRunner{}
	r.add(
		fakeStep{match: matchHas("docker", "inspect"), err: errors.New("missing")},
		fakeStep{match: matchHas("docker", "run")},
		fakeStep{match: matchHas("docker", "inspect"), stdout: []string{"n "}},
		fakeStep{match: matchHas("docker", "connect")},
		fakeStep{match: matchHas("docker", "reload")},
	)
	ing, err := New(Config{
		StateDir: t.TempDir(),
		Runner:   r,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()
	url, err := ing.Route(context.Background(), "app", "app.example.com", "web:80")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://app.example.com" {
		t.Fatalf("url = %q", url)
	}
}

func TestCaddyfileWrittenInPlace(t *testing.T) {
	r := &fakeRunner{}
	cfg := testConfig(t, r)
	scriptFirstRoute(r, cfg.ContainerName, "web")
	ing, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()

	path := filepath.Join(cfg.StateDir, "ingress", "Caddyfile")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	fiBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ing.Route(context.Background(), "app", "app.example.com", "web:80"); err != nil {
		t.Fatal(err)
	}
	fiAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same inode means in-place write (not rename-replace).
	if !os.SameFile(fiBefore, fiAfter) {
		t.Fatal("Caddyfile was replaced by rename; must write in place for bind mounts")
	}
}
