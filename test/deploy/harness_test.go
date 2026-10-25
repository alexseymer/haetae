//go:build e2e_deploy

package deploy_test

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
	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	adminEmail     = "admin@htest.example"
	adminPassword  = "a long test passphrase"
	memberEmail    = "member@htest.example"
	memberPassword = "another long passphrase"
	caddyName      = "htest-caddy"
	caddyVol       = "htest-caddy"
)

// harness is one live server + enrolled agent shared by the deploy scenarios.
type harness struct {
	t          *testing.T
	repoRoot   string
	serverBin  string
	agentBin   string
	dbURL      string
	dataDir    string
	agentState string
	webURL     string
	gwURL      string
	httpsPort  string
	httpPort   string
	hostID     string
	serverCmd  *exec.Cmd
	agentCmd   *exec.Cmd
	serverOut  *syncBuffer
	agentOut   *syncBuffer
	admin      *http.Client
	member     *http.Client
	prefix     string // unique htest-<hex> for this run
}

func startHarness(t *testing.T) *harness {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Fatalf("docker required: %v", err)
	}

	root := repoRoot(t)
	h := &harness{
		t:          t,
		repoRoot:   root,
		serverBin:  buildBin(t, root, "haetae-server"),
		agentBin:   buildBin(t, root, "haetae-agent"),
		dataDir:    t.TempDir(),
		agentState: t.TempDir(),
		serverOut:  &syncBuffer{},
		agentOut:   &syncBuffer{},
		prefix:     "htest-" + shortID(),
		httpPort:   freePort(t),
		httpsPort:  freePort(t),
	}
	_, h.dbURL = storetest.NewWithURL(t)

	webPort, gwPort := freePort(t), freePort(t)
	h.webURL = "https://127.0.0.1:" + webPort
	h.gwURL = "https://127.0.0.1:" + gwPort

	setupCodeFile := filepath.Join(h.dataDir, "setup-code")
	h.serverCmd = exec.Command(h.serverBin, "serve")
	h.serverCmd.Env = append(os.Environ(),
		"HAETAE_DATABASE_URL="+h.dbURL,
		"HAETAE_DATA_DIR="+h.dataDir,
		"HAETAE_WEB_ADDR=127.0.0.1:"+webPort,
		"HAETAE_WEB_URL="+h.webURL,
		"HAETAE_GATEWAY_ADDR=127.0.0.1:"+gwPort,
		"HAETAE_GATEWAY_URL="+h.gwURL,
		"HAETAE_ALLOW_LOCAL_GIT=1",
	)
	h.serverCmd.Stdout, h.serverCmd.Stderr = h.serverOut, h.serverOut
	h.serverCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := h.serverCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		killProcessGroup(h.serverCmd)
		if t.Failed() {
			t.Logf("server output:\n%s", h.serverOut.String())
			t.Logf("agent output:\n%s", h.agentOut.String())
		}
	})

	waitFor(t, 30*time.Second, "web up", func() bool {
		resp, err := insecureClient(nil).Get(h.webURL + "/healthz")
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

	codeBytes, err := os.ReadFile(setupCodeFile)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(string(codeBytes))

	h.admin = insecureClient(t)
	if status, body := h.api(h.admin, "POST", "/api/setup", map[string]string{
		"setupCode": code, "orgName": "DeployTest", "email": adminEmail, "password": adminPassword,
	}); status != http.StatusCreated {
		t.Fatalf("setup: %d %s", status, body)
	}
	if status, body := h.api(h.admin, "POST", "/api/login", map[string]string{
		"email": adminEmail, "password": adminPassword,
	}); status != 200 {
		t.Fatalf("admin login: %d %s", status, body)
	}
	if status, body := h.api(h.admin, "POST", "/api/users", map[string]string{
		"email": memberEmail, "password": memberPassword, "role": "member",
	}); status != http.StatusCreated {
		t.Fatalf("create member: %d %s", status, body)
	}
	h.member = insecureClient(t)
	if status, body := h.api(h.member, "POST", "/api/login", map[string]string{
		"email": memberEmail, "password": memberPassword,
	}); status != 200 {
		t.Fatalf("member login: %d %s", status, body)
	}

	var enr struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if status, body := h.api(h.admin, "POST", "/api/hosts/enrollment", map[string]any{
		"name": h.prefix + "-host", "ttlHours": 1,
	}, &enr); status != http.StatusCreated {
		t.Fatalf("enrollment: %d %s", status, body)
	}
	caPin := regexp.MustCompile(`--ca-pin '([^']+)'`).FindStringSubmatch(enr.Command)
	if caPin == nil {
		t.Fatalf("no CA pin in enrollment command:\n%s", enr.Command)
	}

	enroll := exec.Command(h.agentBin, "enroll",
		"--server", h.gwURL,
		"--token", enr.Token,
		"--ca-pin", caPin[1],
		"--state-dir", h.agentState,
	)
	if out, err := enroll.CombinedOutput(); err != nil {
		t.Fatalf("agent enroll: %v\n%s", err, out)
	}

	h.agentCmd = exec.Command(h.agentBin, "run",
		"--state-dir", h.agentState,
		"--ingress-http-port", h.httpPort,
		"--ingress-https-port", h.httpsPort,
	)
	h.agentCmd.Env = append(os.Environ(),
		"HAETAE_AGENT_ALLOW_LOCAL_GIT=1",
		"HAETAE_AGENT_CADDY_TLS=internal",
		"HAETAE_AGENT_CADDY_NAME="+caddyName,
		"HAETAE_AGENT_CADDY_VOLUME_PREFIX="+caddyVol,
		"HAETAE_AGENT_HEALTH_WINDOW=2s",
		"HAETAE_AGENT_WATCH_INTERVAL=2s",
		"HAETAE_AGENT_START_TIMEOUT=45s",
	)
	h.agentCmd.Stdout, h.agentCmd.Stderr = h.agentOut, h.agentOut
	h.agentCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := h.agentCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		killProcessGroup(h.agentCmd)
		// Independent of test context: tear down shared ingress, then any
		// leftover project networks Caddy was still attached to.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", caddyName).Run()
		_ = exec.CommandContext(ctx, "docker", "volume", "rm", "-f", caddyVol+"-data", caddyVol+"-config").Run()
		_ = dockerclean.ForceRemove(h.prefix)
		_ = dockerclean.ForceRemove(caddyName)
		_ = dockerclean.ForceRemove(caddyVol)
	})

	waitFor(t, 30*time.Second, "host connected", func() bool {
		var hosts struct {
			Hosts []struct {
				ID        string `json:"id"`
				Connected bool   `json:"connected"`
			} `json:"hosts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts", nil, &hosts)
		if status != 200 || len(hosts.Hosts) != 1 {
			return false
		}
		if hosts.Hosts[0].Connected {
			h.hostID = hosts.Hosts[0].ID
			return true
		}
		return false
	})
	return h
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
		// API middleware requires application/json on mutating requests.
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

func (h *harness) createProject(body map[string]any) map[string]any {
	h.t.Helper()
	var out struct {
		Project map[string]any `json:"project"`
	}
	status, raw := h.api(h.admin, "POST", "/api/hosts/"+h.hostID+"/projects", body, &out)
	if status != http.StatusCreated {
		h.t.Fatalf("create project: %d %s", status, raw)
	}
	name, _ := out.Project["name"].(string)
	h.trackProject(name)
	return out.Project
}

func (h *harness) patchProject(pid string, body map[string]any) map[string]any {
	h.t.Helper()
	var out struct {
		Project map[string]any `json:"project"`
	}
	status, raw := h.api(h.admin, "PATCH", "/api/projects/"+pid, body, &out)
	if status != 200 {
		h.t.Fatalf("patch project: %d %s", status, raw)
	}
	return out.Project
}

func (h *harness) startDeploy(pid string) map[string]any {
	h.t.Helper()
	var out struct {
		Deploy map[string]any `json:"deploy"`
	}
	status, raw := h.api(h.admin, "POST", "/api/projects/"+pid+"/deploy", map[string]any{}, &out)
	if status != http.StatusAccepted {
		h.t.Fatalf("start deploy: %d %s", status, raw)
	}
	return out.Deploy
}

func (h *harness) getDeploy(did string) map[string]any {
	h.t.Helper()
	var out struct {
		Deploy map[string]any `json:"deploy"`
	}
	status, raw := h.api(h.admin, "GET", "/api/deploys/"+did, nil, &out)
	if status != 200 {
		h.t.Fatalf("get deploy: %d %s", status, raw)
	}
	return out.Deploy
}

func (h *harness) waitDeploy(t *testing.T, did string, want string, timeout time.Duration) (map[string]any, time.Duration) {
	t.Helper()
	start := time.Now()
	var last map[string]any
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		last = h.getDeploy(did)
		got, _ := last["status"].(string)
		if got == want {
			return last, time.Since(start)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for deploy %s status=%s; last=%v", timeout, did, want, last)
	return last, time.Since(start)
}

func (h *harness) waitDeployAny(t *testing.T, did string, want []string, timeout time.Duration) (map[string]any, time.Duration) {
	t.Helper()
	start := time.Now()
	set := map[string]bool{}
	for _, w := range want {
		set[w] = true
	}
	var last map[string]any
	waitFor(t, timeout, fmt.Sprintf("deploy %s in %v", did, want), func() bool {
		last = h.getDeploy(did)
		got, _ := last["status"].(string)
		return set[got]
	})
	return last, time.Since(start)
}

func (h *harness) latestDeploy(pid string) map[string]any {
	h.t.Helper()
	var out struct {
		Deploys []map[string]any `json:"deploys"`
	}
	status, raw := h.api(h.admin, "GET", "/api/projects/"+pid+"/deploys", nil, &out)
	if status != 200 {
		h.t.Fatalf("list deploys: %d %s", status, raw)
	}
	if len(out.Deploys) == 0 {
		h.t.Fatal("no deploys")
	}
	return out.Deploys[0]
}

func (h *harness) deployLogs(did string) string {
	h.t.Helper()
	var all strings.Builder
	after := 0
	for {
		var page struct {
			Logs []struct {
				Seq  int    `json:"seq"`
				Text string `json:"text"`
			} `json:"logs"`
			Done bool `json:"done"`
		}
		status, raw := h.api(h.admin, "GET", fmt.Sprintf("/api/deploys/%s/logs?after=%d&limit=500", did, after), nil, &page)
		if status != 200 {
			h.t.Fatalf("deploy logs: %d %s", status, raw)
		}
		for _, l := range page.Logs {
			all.WriteString(l.Text)
			all.WriteByte('\n')
			if l.Seq > after {
				after = l.Seq
			}
		}
		if page.Done || len(page.Logs) == 0 {
			break
		}
	}
	return all.String()
}

func (h *harness) dbLogTexts(did string) string {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, h.dbURL)
	if err != nil {
		h.t.Fatal(err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT text FROM deploy_logs WHERE deploy_id = $1 ORDER BY seq`, did)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			h.t.Fatal(err)
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	return b.String()
}

func (h *harness) httpsGet(host, path string) (int, string) {
	h.t.Helper()
	// Dial 127.0.0.1 but keep the domain in the URL/SNI so Caddy selects the right site.
	addr := "127.0.0.1:" + h.httpsPort
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec // test: Caddy tls internal
			ServerName:         host,
		},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (h *harness) waitHTTPS(t *testing.T, host, path string, wantStatus int, wantBody string, timeout time.Duration) {
	t.Helper()
	var lastCode int
	var lastBody string
	waitFor(t, timeout, fmt.Sprintf("HTTPS %s %s -> %d", host, path, wantStatus), func() bool {
		lastCode, lastBody = h.httpsGet(host, path)
		if lastCode != wantStatus {
			return false
		}
		return wantBody == "" || strings.Contains(lastBody, wantBody)
	})
	if t.Failed() {
		t.Logf("last HTTPS response: %d %q", lastCode, lastBody)
	}
}

// trackProject registers cleanup that removes compose resources for name.
func (h *harness) trackProject(name string) {
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "compose", "-p", name, "down", "--remove-orphans").Run()
		out, _ := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+name).Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		}
		// Caddy stays attached to project networks until disconnect/rm.
		_ = exec.CommandContext(ctx, "docker", "network", "disconnect", "-f", name+"_default", caddyName).Run()
		_ = dockerclean.ForceRemove(name)
	})
}

func (h *harness) deleteProject(t *testing.T, pid string) {
	t.Helper()
	status, raw := h.api(h.admin, "DELETE", "/api/projects/"+pid, nil)
	if status != http.StatusNoContent && status != 200 {
		t.Fatalf("delete project: %d %s", status, raw)
	}
}

func (h *harness) projName(suffix string) string {
	// project names: 1–40 chars, lower-case / digits / dashes
	name := h.prefix + "-" + suffix
	if len(name) > 40 {
		name = name[:40]
	}
	return name
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
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test: server/Caddy use internal CAs
		},
		Timeout: 20 * time.Second,
	}
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
