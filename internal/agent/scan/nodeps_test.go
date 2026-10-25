package scan_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// The resident agent must not link Syft; only haetae-inventory may.
func TestAgentDoesNotDependOnSyft(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/haetae-agent")
	cmd.Dir = repoRoot(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.Contains(line, "github.com/anchore/syft") {
			t.Fatalf("haetae-agent depends on Syft via %q (must stay in haetae-inventory only)", line)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// This file lives in internal/agent/scan → three levels up.
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
