package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("HELPER_MODE")
	switch mode {
	case "ok":
		doc := HelperDocument{
			ToolVersion:  "test-1",
			ImageID:      "sha256:abc",
			Distro:       HelperDistro{Name: "alpine", Version: "3.20"},
			Packages:     []HelperPkg{{Name: "musl", Version: "1.2.5", Type: "apk", PURL: "pkg:apk/musl@1.2.5"}},
			PeakRSSBytes: 12345,
			StartedMs:    1,
			FinishedMs:   2,
		}
		_ = json.NewEncoder(os.Stdout).Encode(doc)
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	case "unsupported":
		fmt.Fprintln(os.Stderr, "cannot read image")
		os.Exit(2)
	case "toomany":
		fmt.Fprintln(os.Stderr, "too many packages")
		os.Exit(3)
	case "timeout":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "malformed":
		fmt.Fprintln(os.Stdout, "{not-json")
		os.Exit(0)
	case "toolarge":
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 9; i++ {
			_, _ = os.Stdout.WriteString(chunk)
		}
		os.Exit(0)
	case "alloc": // touch 512 MiB, far above the 64 MiB test limit
		var keep [][]byte
		for i := 0; i < 512; i++ {
			b := make([]byte, 1<<20)
			for j := range b {
				b[j] = 1
			}
			keep = append(keep, b)
		}
		_ = keep
		os.Exit(0)
	case "orphan": // leave a grandchild behind in the helper's cgroup
		c := exec.Command("sleep", "300")
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			os.Exit(1)
		}
		_ = os.WriteFile(os.Getenv("HELPER_PIDFILE"), []byte(strconv.Itoa(c.Process.Pid)), 0o644)
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "unknown HELPER_MODE")
		os.Exit(1)
	}
}

func mustFakeCgroup(t *testing.T, root, events, peak string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte{}, 0o644)
	_ = os.WriteFile(filepath.Join(root, "memory.events"), []byte(events), 0o644)
	_ = os.WriteFile(filepath.Join(root, "memory.peak"), []byte(peak+"\n"), 0o644)
}

func writeHelperShim(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "haetae-inventory")
	self, err := exec.LookPath(os.Args[0])
	if err != nil {
		self = os.Args[0]
	}
	content := fmt.Sprintf("#!/bin/sh\nexport GO_WANT_HELPER_PROCESS=1\nexport HELPER_MODE=%s\nexport HELPER_PIDFILE=%q\nexec %q -test.run=^TestHelperProcess$ -- \"$@\"\n", mode, filepath.Join(dir, "pid"), self)
	if err := os.WriteFile(shim, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

func launcherFor(t *testing.T, mode, events, peak string) *Launcher {
	t.Helper()
	l := NewLauncher()
	l.HelperPath = writeHelperShim(t, mode)
	l.Timeout = 5 * time.Second
	root := filepath.Join(t.TempDir(), "cg")
	mustFakeCgroup(t, root, events, peak)
	l.CgroupRoot = root
	return l
}

func TestLauncherOK(t *testing.T) {
	res := launcherFor(t, "ok", "oom_kill 0\n", "999").Run(context.Background(), LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != StatusOK {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	if res.Doc == nil || res.Doc.ImageID != "sha256:abc" {
		t.Fatalf("doc=%#v", res.Doc)
	}
	if !res.Limited {
		t.Fatal("expected limited=true with fake cgroup")
	}
}

func TestLauncherFailed(t *testing.T) {
	res := launcherFor(t, "fail", "oom_kill 0\n", "1").Run(context.Background(), LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != StatusFailed {
		t.Fatalf("status=%s", res.Status)
	}
}

func TestLauncherUnsupported(t *testing.T) {
	res := launcherFor(t, "unsupported", "oom_kill 0\n", "1").Run(context.Background(), LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != StatusUnsupported {
		t.Fatalf("status=%s", res.Status)
	}
}

func TestLauncherTimeout(t *testing.T) {
	l := launcherFor(t, "timeout", "oom_kill 0\n", "1")
	l.Timeout = 200 * time.Millisecond
	start := time.Now()
	res := l.Run(context.Background(), LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != StatusTimeout {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout took too long")
	}
}

func TestLauncherOOM(t *testing.T) {
	res := launcherFor(t, "ok", "oom_kill 1\n", "5000").Run(context.Background(), LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != StatusOOM {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	if res.PeakMemory != 5000 {
		t.Fatalf("peak=%d", res.PeakMemory)
	}
}

func TestLauncherMalformedJSON(t *testing.T) {
	res := launcherFor(t, "malformed", "oom_kill 0\n", "1").Run(context.Background(), LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != StatusFailed || !strings.Contains(res.Error, "malformed") {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
}

func TestLauncherTooLargeOutput(t *testing.T) {
	l := launcherFor(t, "toolarge", "oom_kill 0\n", "1")
	l.Timeout = 15 * time.Second
	res := l.Run(context.Background(), LaunchRequest{DockerImage: "alpine:3.20"})
	if res.Status != StatusFailed || !res.RawTooLarge {
		t.Fatalf("status=%s rawTooLarge=%v err=%s", res.Status, res.RawTooLarge, res.Error)
	}
}

func TestValidateRejectsShellMeta(t *testing.T) {
	if err := validateImageArg("alpine;rm -rf /"); err == nil {
		t.Fatal("expected error")
	}
	if err := validateRegistryArg("alpine@sha256:deadbeef"); err != nil {
		t.Fatal(err)
	}
	if err := validateRegistryArg("alpine:3.20"); err == nil {
		t.Fatal("expected digest required")
	}
}

func TestParseBytes(t *testing.T) {
	n, err := parseBytes("256m")
	if err != nil || n != 256<<20 {
		t.Fatalf("%d %v", n, err)
	}
	n, err = parseBytes(strconv.Itoa(1000))
	if err != nil || n != 1000 {
		t.Fatalf("%d %v", n, err)
	}
}

// The helper's child must not outlive a timeout: it holds the output pipes,
// and Wait would block until it exits.
func TestLauncherTimeoutKillsChildren(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "haetae-inventory")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nsleep 30\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := NewLauncher()
	l.HelperPath = shim
	l.Timeout = 500 * time.Millisecond
	l.CgroupRoot = filepath.Join(dir, "cg")
	mustFakeCgroup(t, l.CgroupRoot, "oom_kill 0\n", "1")
	start := time.Now()
	res := l.Run(context.Background(), LaunchRequest{DockerImage: "sha256:" + strings.Repeat("c", 64)})
	if res.Status != StatusTimeout {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("timeout took %s; a child kept the pipe open", d)
	}
}
