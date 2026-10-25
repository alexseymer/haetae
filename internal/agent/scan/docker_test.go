//go:build deploy

package scan_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/agent/scan"
)

func TestRealDockerInventory(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	root := repoRoot(t)
	ensureAlpine(t)

	helper := filepath.Join(t.TempDir(), "haetae-inventory")
	build := exec.Command("go", "build", "-o", helper, "./cmd/haetae-inventory")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}

	l := scan.NewLauncher()
	l.HelperPath = helper
	l.Timeout = 5 * time.Minute
	l.MemBytes = 256 << 20

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res := l.Run(ctx, scan.LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != scan.StatusOK {
		t.Fatalf("status=%s err=%s limited=%v", res.Status, res.Error, res.Limited)
	}
	if res.Doc == nil {
		t.Fatal("nil document")
	}
	found := false
	for _, p := range res.Doc.Packages {
		name := strings.ToLower(p.Name)
		if (name == "musl" || name == "busybox") && p.Version != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("packages missing musl/busybox: %+v", res.Doc.Packages)
	}
}

func ensureAlpine(t *testing.T) {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", "alpine:3.20").CombinedOutput()
	if err == nil {
		return
	}
	t.Logf("alpine:3.20 missing (%v); pulling", err)
	cmd := exec.Command("docker", "pull", "alpine:3.20")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker pull alpine:3.20: %v\n%s", err, out)
	}
}
