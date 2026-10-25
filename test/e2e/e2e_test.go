//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store/storetest"
)

// TestWebUIE2E builds the embedded UI and real binaries, starts Postgres and
// haetae-server, runs a noisy busybox container, then drives the browser
// acceptance suite in ui.spec.mjs against the live server.
func TestWebUIE2E(t *testing.T) {
	repoRoot := repoRoot(t)
	e2eDir := filepath.Join(repoRoot, "test", "e2e")
	shotDir := filepath.Join(e2eDir, "out")
	if err := os.MkdirAll(shotDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ensureWebBuild(t, repoRoot)
	serverBin := buildBin(t, repoRoot, "haetae-server")
	agentBin := buildBin(t, repoRoot, "haetae-agent")
	linkPlaywright(t, e2eDir)

	_, dbURL := storetest.NewWithURL(t)
	dataDir := t.TempDir()
	agentState := t.TempDir()
	serverLog := filepath.Join(t.TempDir(), "server.log")
	setupCodeFile := filepath.Join(dataDir, "setup-code")

	webPort, gwPort := freePort(t), freePort(t)
	webURL := "https://127.0.0.1:" + webPort
	gwURL := "https://127.0.0.1:" + gwPort

	logFile, err := os.Create(serverLog)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	serverOut := &syncBuffer{}
	serverW := io.MultiWriter(logFile, serverOut)

	server := exec.CommandContext(ctx, serverBin, "serve")
	server.Env = append(os.Environ(),
		"HAETAE_DATABASE_URL="+dbURL,
		"HAETAE_DATA_DIR="+dataDir,
		"HAETAE_WEB_ADDR=127.0.0.1:"+webPort,
		"HAETAE_WEB_URL="+webURL,
		"HAETAE_GATEWAY_ADDR=127.0.0.1:"+gwPort,
		"HAETAE_GATEWAY_URL="+gwURL,
	)
	server.Stdout, server.Stderr = serverW, serverW
	server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer killProcessGroup(server)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server output:\n%s", serverOut.String())
		}
	})

	waitFor(t, 30*time.Second, "web interface up", func() bool {
		resp, err := insecureClient().Get(webURL + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	waitFor(t, 10*time.Second, "setup-code file", func() bool {
		_, err := os.Stat(setupCodeFile)
		return err == nil
	})

	cid := startBusyboxLogger(t)
	defer func() { _ = exec.Command("docker", "rm", "-f", cid).Run() }()

	node := exec.CommandContext(ctx, "node", "--test", "--test-concurrency=1", filepath.Join(e2eDir, "ui.spec.mjs"))
	node.Dir = repoRoot
	node.Env = append(os.Environ(),
		"E2E_WEB_URL="+webURL,
		"E2E_SETUP_CODE_FILE="+setupCodeFile,
		"E2E_AGENT_BIN="+agentBin,
		"E2E_AGENT_STATE_DIR="+agentState,
		"E2E_SERVER_LOG_FILE="+serverLog,
		"E2E_SHOT_DIR="+shotDir,
		"E2E_GATEWAY_URL="+gwURL,
		"E2E_CHROMIUM=/usr/bin/chromium",
		"NODE_PATH="+npmRootGlobal(t),
	)
	node.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := node.CombinedOutput()
	t.Logf("node output:\n%s", out)
	if err != nil {
		t.Fatalf("ui.spec.mjs failed: %v\n%s", err, out)
	}
}

func ensureWebBuild(t *testing.T, repoRoot string) {
	t.Helper()
	index := filepath.Join(repoRoot, "internal", "webui", "app", "index.html")
	if os.Getenv("HAETAE_E2E_SKIP_WEB_BUILD") == "1" {
		if _, err := os.Stat(index); err == nil {
			t.Log("skipping make web (HAETAE_E2E_SKIP_WEB_BUILD=1 and index.html present)")
			return
		}
		t.Log("HAETAE_E2E_SKIP_WEB_BUILD=1 but index.html missing; building web")
	}
	cmd := exec.Command("make", "web")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make web: %v\n%s", err, out)
	}
}

func buildBin(t *testing.T, repoRoot, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/"+name)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return bin
}

// linkPlaywright makes `import { chromium } from 'playwright'` resolve from
// ui.spec.mjs. Node ESM ignores NODE_PATH; a local node_modules symlink to the
// global install (npm root -g) is enough. Nested playwright-core stays inside
// that package.
func linkPlaywright(t *testing.T, e2eDir string) {
	t.Helper()
	root := npmRootGlobal(t)
	src := filepath.Join(root, "playwright")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("playwright not found at %s (npm root -g); install playwright globally or the Debian node-playwright package: %v", src, err)
	}
	nm := filepath.Join(e2eDir, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(nm, "playwright")
	_ = os.Remove(dst)
	if err := os.Symlink(src, dst); err != nil {
		t.Fatalf("symlink playwright: %v", err)
	}
}

func npmRootGlobal(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("npm", "root", "-g").Output()
	if err != nil {
		t.Fatalf("npm root -g: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func startBusyboxLogger(t *testing.T) string {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Fatalf("docker required for e2e: %v", err)
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "haetae-e2e-" + hex.EncodeToString(b[:])
	script := `while true; do
  echo 'level=error msg="payment failed for order 1234"'
  echo 'level=info msg="heartbeat ok"'
  echo 'level=error <script>window.__xss=1</script> <img src=x onerror=window.__xss=1>'
  sleep 2
done`
	_ = exec.Command("docker", "pull", "-q", "busybox:1.37").Run()
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"--log-opt", "max-size=5m",
		"busybox:1.37", "sh", "-c", script,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// go test runs with cwd = package dir (test/e2e).
	root := filepath.Clean(filepath.Join(wd, "../.."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root (from %s): %v", wd, err)
	}
	return root
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	return addr[strings.LastIndex(addr, ":")+1:]
}

func insecureClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test: server uses its own CA
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_, _ = cmd.Process.Wait()
}

// syncBuffer is safe to read while a process writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
