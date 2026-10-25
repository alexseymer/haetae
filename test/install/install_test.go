//go:build integration

package install_test

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store/storetest"
)

const curlImage = "curlimages/curl:8.11.1"

// serverProc is the real haetae-server binary running against a test database.
// Copied from test/integration (same package cannot be imported).
type serverProc struct {
	t       *testing.T
	cmd     *exec.Cmd
	webURL  string
	gwURL   string
	dataDir string
	out     *syncBuffer
}

func buildServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "haetae-server")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/haetae-server").CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	return bin
}

func startServer(t *testing.T, extraEnv ...string) *serverProc {
	t.Helper()
	requireDistDirWiring(t)
	_, dbURL := storetest.NewWithURL(t)
	p := &serverProc{
		t: t, dataDir: t.TempDir(), out: &syncBuffer{},
		webURL: "https://127.0.0.1:" + freePort(t), gwURL: "https://127.0.0.1:" + freePort(t),
	}
	p.cmd = exec.Command(buildServer(t), "serve")
	p.cmd.Env = append(os.Environ(),
		"HAETAE_DATABASE_URL="+dbURL,
		"HAETAE_DATA_DIR="+p.dataDir,
		"HAETAE_WEB_ADDR="+strings.TrimPrefix(p.webURL, "https://"),
		"HAETAE_WEB_URL="+p.webURL,
		"HAETAE_GATEWAY_ADDR="+strings.TrimPrefix(p.gwURL, "https://"),
		"HAETAE_GATEWAY_URL="+p.gwURL,
	)
	p.cmd.Env = append(p.cmd.Env, extraEnv...)
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		if t.Failed() {
			t.Logf("server output:\n%s", p.out.String())
		}
	})
	waitFor(t, 30*time.Second, "web interface up", func() bool {
		resp, err := p.insecureClient().Get(p.webURL + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	return p
}

func freePort(t *testing.T) string {
	t.Helper()
	a := freeAddr(t)
	return a[strings.LastIndex(a, ":")+1:]
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
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

func (p *serverProc) insecureClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test: pin checked separately
}

func (p *serverProc) call(c *http.Client, method, path string, body, out any) int {
	p.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, p.webURL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-Haetae-Request", "1")
	}
	resp, err := c.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			p.t.Fatalf("%s %s: %v\n%s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

func requireDistDirWiring(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile("../../cmd/haetae-server/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(b), "HAETAE_DIST_DIR") {
		t.Skip("HAETAE_DIST_DIR not wired in cmd/haetae-server/main.go yet (pending worker B)")
	}
}

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is unavailable; skipping install script container tests")
	}
}

// buildDist builds linux/amd64 and linux/arm64 agent binaries into a temp
// directory and writes a matching SHA256SUMS. The arm64 binary is built only
// (never executed).
func buildDist(t *testing.T) string {
	t.Helper()
	dist := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		out := filepath.Join(dist, "haetae-agent-linux-"+arch)
		cmd := exec.Command("go", "build", "-o", out, "../../cmd/haetae-agent")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		if o, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build agent %s: %v\n%s", arch, err, o)
		}
	}
	writeSums(t, dist, false)
	return dist
}

func writeSums(t *testing.T, dist string, tamper bool) {
	t.Helper()
	var b strings.Builder
	for _, name := range []string{"haetae-agent-linux-amd64", "haetae-agent-linux-arm64"} {
		raw, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if tamper {
			sum[0] ^= 0xff
		}
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	if err := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupAdminAndEnrollment(t *testing.T, p *serverProc, hostName string) (command string) {
	t.Helper()
	c := p.insecureClient()
	var status struct {
		SetupRequired bool `json:"setupRequired"`
	}
	if p.call(c, "GET", "/api/status", nil, &status) != 200 || !status.SetupRequired {
		t.Fatalf("status = %+v", status)
	}
	raw, err := os.ReadFile(filepath.Join(p.dataDir, "setup-code"))
	if err != nil {
		t.Fatalf("no setup-code file: %v", err)
	}
	code := strings.TrimSpace(string(raw))
	if s := p.call(c, "POST", "/api/setup", map[string]string{
		"setupCode": code, "orgName": "Acme", "email": "admin@example.com", "password": "a long test passphrase",
	}, nil); s != http.StatusCreated {
		t.Fatalf("setup = %d", s)
	}
	var enr struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if s := p.call(c, "POST", "/api/hosts/enrollment", map[string]any{"name": hostName, "ttlHours": 1}, &enr); s != http.StatusCreated {
		t.Fatalf("enrollment = %d", s)
	}
	if enr.Command == "" {
		t.Fatal("empty install command")
	}
	return enr.Command
}

func dockerInstall(t *testing.T, script string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command("docker", "run", "--rm", "--network", "host", "--user", "root",
		"-e", "HAETAE_PREFIX=/root-fs",
		"-e", "HAETAE_SKIP_SYSTEMD=1",
		curlImage, "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallScriptEnrollsAnAgent(t *testing.T) {
	requireDocker(t)
	dist := buildDist(t)
	p := startServer(t, "HAETAE_DIST_DIR="+dist)
	command := setupAdminAndEnrollment(t, p, "install-host")

	script := command +
		` && test -f /root-fs/var/lib/haetae-agent/identity.json` +
		` && test "$(stat -c %a /root-fs/usr/local/bin/haetae-agent)" = 755` +
		` && test "$(stat -c %a /root-fs/var/lib/haetae-agent)" = 700` +
		` && cat /root-fs/var/lib/haetae-agent/identity.json`
	out, err := dockerInstall(t, script)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"host_id"`) {
		t.Fatalf("identity.json missing host_id:\n%s", out)
	}
}

func TestInstallScriptRejectsATamperedBinary(t *testing.T) {
	requireDocker(t)
	dist := buildDist(t)
	writeSums(t, dist, true)
	p := startServer(t, "HAETAE_DIST_DIR="+dist)
	command := setupAdminAndEnrollment(t, p, "tamper-host")

	script := `set -o pipefail; ` + command +
		`; ec=$?; if [ -e /root-fs/usr/local/bin/haetae-agent ] || [ -e /root-fs/var/lib/haetae-agent/identity.json ]; then echo INSTALLED; exit 99; fi; exit $ec`
	out, err := dockerInstall(t, script)
	if err == nil {
		t.Fatalf("expected non-zero exit for tampered binary; out:\n%s", out)
	}
	if strings.Contains(out, "INSTALLED") {
		t.Fatalf("tampered binary was installed:\n%s", out)
	}
}

func TestInstallScriptRefusesAWrongWebPin(t *testing.T) {
	requireDocker(t)
	dist := buildDist(t)
	p := startServer(t, "HAETAE_DIST_DIR="+dist)
	command := setupAdminAndEnrollment(t, p, "badpin-host")

	wrong := "sha256//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	bad := command
	// Replace every occurrence of the real pin with a wrong one (curl fetch and --web-pin).
	if !strings.Contains(bad, "--web-pin") {
		t.Fatal("expected --web-pin in install command (self-signed web cert)")
	}
	// Extract the quoted pin after --pinnedpubkey and --web-pin.
	bad = replaceQuotedFlag(bad, "--pinnedpubkey", wrong)
	bad = replaceQuotedFlag(bad, "--web-pin", wrong)

	// pipefail so a failing curl on the left of `| sh -s` makes the whole
	// pipeline non-zero (default ash exits with the right-hand status).
	script := `set -o pipefail; ` + bad +
		`; ec=$?; if [ -e /root-fs/usr/local/bin/haetae-agent ] || [ -e /root-fs/var/lib/haetae-agent/identity.json ]; then echo INSTALLED; exit 99; fi; exit $ec`
	out, err := dockerInstall(t, script)
	if err == nil {
		t.Fatalf("expected failure with wrong web pin; out:\n%s", out)
	}
	if strings.Contains(out, "INSTALLED") {
		t.Fatalf("install proceeded with wrong web pin:\n%s", out)
	}
}

func TestInstallScriptIsIdempotent(t *testing.T) {
	requireDocker(t)
	dist := buildDist(t)
	p := startServer(t, "HAETAE_DIST_DIR="+dist)
	command := setupAdminAndEnrollment(t, p, "idempotent-host")

	// Both runs share one container so /root-fs (and identity.json) survive.
	// The second run must succeed even though the token is already used, and
	// must skip enroll rather than calling the agent again.
	script := command +
		` && out2=$(` + command + ` 2>&1)` +
		` && echo "$out2" | grep -q '[Aa]lready enrolled'` +
		` && echo "$out2" | grep -qv 'Enrolling with the control plane'` +
		` && test -f /root-fs/var/lib/haetae-agent/identity.json` +
		` && echo IDEMPOTENT_OK`
	out, err := dockerInstall(t, script)
	if err != nil {
		t.Fatalf("idempotent install failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "IDEMPOTENT_OK") {
		t.Fatalf("missing success marker:\n%s", out)
	}
}

func TestInstallScriptShellcheck(t *testing.T) {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Skip("shellcheck not installed")
	}
	cmd := exec.Command("shellcheck", "-s", "sh", "../../internal/installer/agent-install.sh")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck: %v\n%s", err, out)
	}
}

// replaceQuotedFlag replaces the single-quoted value of flag (e.g. --web-pin '...') with want.
func replaceQuotedFlag(cmd, flag, want string) string {
	needle := flag + " '"
	i := strings.Index(cmd, needle)
	if i < 0 {
		return cmd
	}
	start := i + len(needle)
	end := strings.Index(cmd[start:], "'")
	if end < 0 {
		return cmd
	}
	end += start
	return cmd[:start] + want + cmd[end:]
}
