//go:build e2e_deploy

package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo is a local bare repo plus a working tree, reachable via file://.
type gitRepo struct {
	t       *testing.T
	bare    string
	work    string
	fileURL string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "bare.git")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "init", "--bare", bare)
	runGit(t, work, "init", "-b", "main")
	runGit(t, work, "config", "user.email", "deploy-e2e@htest.local")
	runGit(t, work, "config", "user.name", "Deploy E2E")
	runGit(t, work, "remote", "add", "origin", bare)
	return &gitRepo{t: t, bare: bare, work: work, fileURL: "file://" + bare}
}

func (g *gitRepo) write(path, content string) {
	g.t.Helper()
	full := filepath.Join(g.work, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

func (g *gitRepo) commitAll(msg string) string {
	g.t.Helper()
	runGit(g.t, g.work, "add", "-A")
	runGit(g.t, g.work, "commit", "--allow-empty", "-m", msg)
	runGit(g.t, g.work, "push", "-u", "origin", "HEAD:main")
	out := runGit(g.t, g.work, "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
