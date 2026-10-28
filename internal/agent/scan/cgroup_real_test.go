//go:build linux

package scan

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestRealCgroupDelegation runs against the real cgroup v2 hierarchy. It needs
// a delegated cgroup, as the agent's systemd unit provides; run it with
//
//	sudo systemd-run --wait --pipe -p User=$USER -p Delegate=yes \
//	  -p MemoryMax=1G -E HAETAE_REAL_CGROUP=1 ./scan.test -test.run TestRealCgroup -test.v
func TestRealCgroupDelegation(t *testing.T) {
	if os.Getenv("HAETAE_REAL_CGROUP") != "1" {
		t.Skip("set HAETAE_REAL_CGROUP=1 inside a delegated systemd unit")
	}

	t.Run("oom inside leaf", func(t *testing.T) {
		l := NewLauncher()
		l.HelperPath = writeHelperShim(t, "alloc")
		l.Timeout = 30 * time.Second
		l.MemBytes = 64 << 20
		res := l.Run(context.Background(), LaunchRequest{DockerImage: "sha256:" + strings.Repeat("a", 64)})
		if res.Status != StatusOOM || !res.Limited {
			t.Fatalf("status=%s limited=%v err=%s", res.Status, res.Limited, res.Error)
		}
		t.Logf("peak=%d", res.PeakMemory)
	})

	t.Run("agent moved to leaf with limits", func(t *testing.T) {
		raw, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			t.Fatal(err)
		}
		rel := strings.TrimPrefix(strings.TrimSpace(string(raw)), "0::")
		if filepath.Base(rel) != agentLeaf {
			t.Fatalf("agent cgroup = %s, want .../%s", rel, agentLeaf)
		}
		max, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", rel, "memory.max"))
		if strings.TrimSpace(string(max)) != strconv.Itoa(agentMemMax) {
			t.Fatalf("agent leaf memory.max = %q", max)
		}
	})

	t.Run("teardown kills stragglers", func(t *testing.T) {
		l := NewLauncher()
		shim := writeHelperShim(t, "orphan")
		l.HelperPath = shim
		l.Timeout = 30 * time.Second
		_ = l.Run(context.Background(), LaunchRequest{DockerImage: "sha256:" + strings.Repeat("b", 64)})
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(shim), "pid"))
		if err != nil {
			t.Fatalf("orphan pid: %v", err)
		}
		pid, _ := strconv.Atoi(string(raw))
		deadline := time.Now().Add(5 * time.Second)
		for unix.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if unix.Kill(pid, 0) == nil {
			_ = unix.Kill(pid, unix.SIGKILL)
			t.Fatalf("orphan %d survived teardown (escaped the leaf)", pid)
		}
		// No inventory leaf left behind.
		dir, _ := delegatedParent("/sys/fs/cgroup")
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "haetae-inventory-") {
				t.Fatalf("leftover leaf %s", e.Name())
			}
		}
	})
}
