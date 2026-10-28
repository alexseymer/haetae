//go:build screenshots

package screenshots_test

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	adminEmail    = "admin@example.com"
	adminPassword = "correct horse battery staple"
	hostName      = "hel1-prod"
)

// harness is one live server + enrolled agent + crafted vuln listing.
type harness struct {
	t            *testing.T
	repoRoot     string
	binDir       string
	serverBin    string
	agentBin     string
	inventoryBin string
	dbURL        string
	dataDir      string
	agentState   string
	webURL       string
	gwURL        string
	webPort      string
	gwPort       string
	hostID       string
	prov         *provider
	serverCmd    *exec.Cmd
	agentCmd     *exec.Cmd
	serverOut    *syncBuffer
	agentOut     *syncBuffer
	admin        *http.Client
	listing      *listingServer
	fixtureDir   string
	pool         *pgxpool.Pool
}

func startHarness(t *testing.T) *harness {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Fatalf("docker required: %v", err)
	}
	ensureImage(t, "nginx:alpine")
	ensureImage(t, "busybox:1.37")
	ensureImage(t, "alpine:3.20")

	root := repoRoot(t)
	binDir := t.TempDir()
	h := &harness{
		t:            t,
		repoRoot:     root,
		binDir:       binDir,
		serverBin:    buildBin(t, root, binDir, "haetae-server"),
		agentBin:     buildBin(t, root, binDir, "haetae-agent"),
		inventoryBin: buildBin(t, root, binDir, "haetae-inventory"),
		dataDir:      t.TempDir(),
		agentState:   t.TempDir(),
		fixtureDir:   t.TempDir(),
		serverOut:    &syncBuffer{},
		agentOut:     &syncBuffer{},
		prov:         startProvider(t),
		listing:      startListingServer(t),
	}
	_, h.dbURL = storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, h.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	h.pool = pool
	t.Cleanup(pool.Close)

	h.webPort, h.gwPort = freePort(t), freePort(t)
	h.webURL = "https://127.0.0.1:" + h.webPort
	h.gwURL = "https://127.0.0.1:" + h.gwPort

	h.startServer(nil, true)
	h.bootstrapAdmin()
	h.enrollAndRunAgent(nil)

	t.Cleanup(func() {
		killProcessGroup(h.agentCmd)
		killProcessGroup(h.serverCmd)
		if t.Failed() {
			t.Logf("server output:\n%s", h.serverOut.String())
			t.Logf("agent output:\n%s", h.agentOut.String())
		}
	})
	return h
}

func (h *harness) serverEnv(extra []string) []string {
	base := filterEnv(os.Environ(), aiEnvPrefixes...)
	env := append(base,
		"HAETAE_DATABASE_URL="+h.dbURL,
		"HAETAE_DATA_DIR="+h.dataDir,
		"HAETAE_WEB_ADDR=127.0.0.1:"+h.webPort,
		"HAETAE_WEB_URL="+h.webURL,
		"HAETAE_GATEWAY_ADDR=127.0.0.1:"+h.gwPort,
		"HAETAE_GATEWAY_URL="+h.gwURL,
		"HAETAE_TEST_HOOKS=1",
		"HAETAE_VULN_DB_URL="+h.listing.URL(),
		"HAETAE_VULN_CHECK_EVERY=5s",
		"HAETAE_AI_PROVIDER=local",
		"HAETAE_AI_BASE_URL="+h.prov.URL()+"/v1",
		"HAETAE_AI_MODEL=gpt-4o-mini",
		"HAETAE_ALLOW_LOCAL_GIT=1",
	)
	return append(env, extra...)
}

var aiEnvPrefixes = []string{
	"HAETAE_AI_", "OPENAI_", "ANTHROPIC_", "AZURE_OPENAI_", "COHERE_", "MISTRAL_",
}

func filterEnv(in []string, prefixes ...string) []string {
	var out []string
	for _, e := range in {
		key, _, _ := strings.Cut(e, "=")
		skip := false
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				skip = true
				break
			}
		}
		if key == "HAETAE_TEST_HOOKS" || key == "HAETAE_VULN_CHECK_EVERY" ||
			key == "HAETAE_SCAN_DAILY_EVERY" || key == "HAETAE_IMAGECHECK_EVERY" ||
			key == "HAETAE_BRIEFING_NOW" || key == "HAETAE_INVENTORY_BIN" ||
			key == "HAETAE_INVENTORY_TIMEOUT" {
			skip = true
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}

func (h *harness) startServer(extra []string, waitSetupCode bool) {
	h.t.Helper()
	h.serverOut = &syncBuffer{}
	h.serverCmd = exec.Command(h.serverBin, "serve")
	h.serverCmd.Env = h.serverEnv(extra)
	h.serverCmd.Stdout, h.serverCmd.Stderr = h.serverOut, h.serverOut
	h.serverCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := h.serverCmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	waitFor(h.t, 30*time.Second, "web up", func() bool {
		resp, err := insecureClient(nil).Get(h.webURL + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	if waitSetupCode {
		setupCodeFile := filepath.Join(h.dataDir, "setup-code")
		waitFor(h.t, 10*time.Second, "setup-code file", func() bool {
			_, err := os.Stat(setupCodeFile)
			return err == nil
		})
	}
}

func (h *harness) bootstrapAdmin() {
	h.t.Helper()
	codeBytes, err := os.ReadFile(filepath.Join(h.dataDir, "setup-code"))
	if err != nil {
		h.t.Fatal(err)
	}
	code := strings.TrimSpace(string(codeBytes))
	h.admin = insecureClient(h.t)
	if status, body := h.api(h.admin, "POST", "/api/setup", map[string]string{
		"setupCode": code, "orgName": "Acme Shop", "email": adminEmail, "password": adminPassword,
	}); status != http.StatusCreated {
		h.t.Fatalf("setup: %d %s", status, body)
	}
	if status, body := h.api(h.admin, "POST", "/api/login", map[string]string{
		"email": adminEmail, "password": adminPassword,
	}); status != 200 {
		h.t.Fatalf("admin login: %d %s", status, body)
	}
}

func (h *harness) enrollAndRunAgent(extraEnv []string) {
	h.t.Helper()
	var enr struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if status, body := h.api(h.admin, "POST", "/api/hosts/enrollment", map[string]any{
		"name": hostName, "ttlHours": 1,
	}, &enr); status != http.StatusCreated {
		h.t.Fatalf("enrollment: %d %s", status, body)
	}
	caPin := regexp.MustCompile(`--ca-pin '([^']+)'`).FindStringSubmatch(enr.Command)
	if caPin == nil {
		h.t.Fatalf("no CA pin in enrollment command:\n%s", enr.Command)
	}
	enroll := exec.Command(h.agentBin, "enroll",
		"--server", h.gwURL,
		"--token", enr.Token,
		"--ca-pin", caPin[1],
		"--state-dir", h.agentState,
	)
	if out, err := enroll.CombinedOutput(); err != nil {
		h.t.Fatalf("agent enroll: %v\n%s", err, out)
	}
	h.startAgent(extraEnv)
	waitFor(h.t, 30*time.Second, "host connected", func() bool {
		var hosts struct {
			Hosts []struct {
				ID        string `json:"id"`
				Connected bool   `json:"connected"`
			} `json:"hosts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts", nil, &hosts)
		if status == 200 && len(hosts.Hosts) == 1 && hosts.Hosts[0].Connected {
			h.hostID = hosts.Hosts[0].ID
			return true
		}
		return false
	})
}

func (h *harness) startAgent(extraEnv []string) {
	h.t.Helper()
	h.agentOut = &syncBuffer{}
	h.agentCmd = exec.Command(h.agentBin, "run", "--state-dir", h.agentState)
	env := append(filterEnv(os.Environ(), aiEnvPrefixes...),
		"HAETAE_TEST_HOOKS=1",
		"HAETAE_INVENTORY_BIN="+h.inventoryBin,
		"HAETAE_AGENT_HEALTH_WINDOW=2s",
		"HAETAE_AGENT_WATCH_INTERVAL=2s",
		"HAETAE_AGENT_ALLOW_LOCAL_GIT=1",
	)
	// Later keys win: drop any base entry overridden by extraEnv.
	env = mergeEnv(env, extraEnv...)
	h.agentCmd.Env = env
	h.agentCmd.Stdout, h.agentCmd.Stderr = h.agentOut, h.agentOut
	h.agentCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := h.agentCmd.Start(); err != nil {
		h.t.Fatal(err)
	}
}

func mergeEnv(base []string, override ...string) []string {
	keys := map[string]bool{}
	for _, e := range override {
		k, _, _ := strings.Cut(e, "=")
		keys[k] = true
	}
	out := make([]string, 0, len(base)+len(override))
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if keys[k] {
			continue
		}
		out = append(out, e)
	}
	return append(out, override...)
}

func (h *harness) api(c *http.Client, method, path string, body any, out ...any) (int, string) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else if method != http.MethodGet && method != http.MethodHead {
		rd = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequest(method, h.webURL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Haetae-Request", "1")
	}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if len(out) > 0 && out[0] != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out[0]); err != nil {
			h.t.Fatalf("%s %s: decode: %v\n%s", method, path, err, b)
		}
	}
	return resp.StatusCode, string(b)
}

// runContainer starts a labelled demo container with a realistic name and
// returns its full id.
func (h *harness) runContainer(name string, args ...string) string {
	h.t.Helper()
	return runDemoContainer(h.t, name, args...)
}

// runDemoContainer starts a demo container. A container of that name that is
// not ours is never touched.
func runDemoContainer(t *testing.T, name string, args ...string) string {
	t.Helper()
	if out, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "name=^"+name+"$").Output(); len(strings.TrimSpace(string(out))) > 0 {
		lbl, _ := exec.Command("docker", "inspect", "-f", `{{index .Config.Labels "dev.haetae.screenshots"}}`, name).Output()
		if strings.TrimSpace(string(lbl)) != "1" {
			t.Fatalf("a container named %q already exists and is not a demo container; remove it first", name)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	cmdArgs := append([]string{"run", "-d", "--name", name, "--label", demoLabel}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run %s: %v\n%s", name, err, out)
	}
	return strings.TrimSpace(string(out))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "../.."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root from %s: %v", wd, err)
	}
	return root
}

func buildBin(t *testing.T, repoRoot, binDir, name string) string {
	t.Helper()
	bin := filepath.Join(binDir, name)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/"+name)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return bin
}

func ensureImage(t *testing.T, ref string) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", ref).Run() == nil {
		return
	}
	t.Logf("pulling %s", ref)
	if out, err := exec.Command("docker", "pull", ref).CombinedOutput(); err != nil {
		t.Fatalf("docker pull %s: %v\n%s", ref, err, out)
	}
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

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func insecureClient(t *testing.T) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test: internal CA
		},
		Timeout: 30 * time.Second,
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_, _ = cmd.Process.Wait()
}

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

func containerIDField(c map[string]any) string {
	if id, ok := c["containerId"].(string); ok && id != "" {
		return id
	}
	if id, ok := c["id"].(string); ok {
		return id
	}
	return ""
}

func shortCID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

var _ = fmt.Sprintf // keep fmt available for scenario files
