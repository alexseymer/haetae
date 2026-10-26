//go:build e2e_operator

package operator_test

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
	adminEmail    = "admin@htest.example"
	adminPassword = "a long test passphrase"
)

// harness is one live server + enrolled agent + scripted AI provider.
type harness struct {
	seenBeforeAsk map[string]bool // chat messages that existed when the last question was posted
	t             *testing.T
	repoRoot      string
	serverBin     string
	agentBin      string
	dbURL         string
	dataDir       string
	agentState    string
	webURL        string
	gwURL         string
	webPort       string
	gwPort        string
	hostID        string
	prefix        string
	serverCmd     *exec.Cmd
	agentCmd      *exec.Cmd
	serverOut     *syncBuffer
	agentOut      *syncBuffer
	admin         *http.Client
	prov          *scriptedProvider
	pool          *pgxpool.Pool
}

func startHarness(t *testing.T) *harness {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Fatalf("docker required: %v", err)
	}
	ensureImage(t, "busybox:1.37")

	root := repoRoot(t)
	binDir := t.TempDir()
	h := &harness{
		t:          t,
		repoRoot:   root,
		serverBin:  buildBin(t, root, binDir, "haetae-server"),
		agentBin:   buildBin(t, root, binDir, "haetae-agent"),
		dataDir:    t.TempDir(),
		agentState: t.TempDir(),
		serverOut:  &syncBuffer{},
		agentOut:   &syncBuffer{},
		prefix:     "htest-" + shortID(),
		prov:       startScriptedProvider(t),
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

	h.startServer()
	h.bootstrapAdmin()
	h.enrollAndRunAgent()
	h.tuneAlerts()

	t.Cleanup(func() {
		killProcessGroup(h.agentCmd)
		killProcessGroup(h.serverCmd)
		for _, n := range h.listNames() {
			_ = exec.Command("docker", "rm", "-f", n).Run()
		}
		if t.Failed() {
			t.Logf("server output:\n%s", h.serverOut.String())
			t.Logf("agent output:\n%s", h.agentOut.String())
		}
	})
	return h
}

func (h *harness) listNames() []string {
	out, _ := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}").Output()
	var res []string
	for _, n := range strings.Fields(string(out)) {
		if strings.HasPrefix(n, h.prefix) {
			res = append(res, n)
		}
	}
	return res
}

var aiEnvPrefixes = []string{
	"HAETAE_AI_", "OPENAI_", "ANTHROPIC_", "AZURE_OPENAI_", "COHERE_", "MISTRAL_",
}

func filterEnv(in []string, prefixes ...string) []string {
	var out []string
	for _, e := range in {
		key, _, _ := strings.Cut(e, "=")
		skip := key == "HAETAE_BRIEFING_NOW" || key == "HAETAE_TEST_HOOKS"
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				skip = true
			}
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}

func (h *harness) startServer() {
	h.t.Helper()
	h.serverCmd = exec.Command(h.serverBin, "serve")
	h.serverCmd.Env = append(filterEnv(os.Environ(), aiEnvPrefixes...),
		"HAETAE_DATABASE_URL="+h.dbURL,
		"HAETAE_DATA_DIR="+h.dataDir,
		"HAETAE_WEB_ADDR=127.0.0.1:"+h.webPort,
		"HAETAE_WEB_URL="+h.webURL,
		"HAETAE_GATEWAY_ADDR=127.0.0.1:"+h.gwPort,
		"HAETAE_GATEWAY_URL="+h.gwURL,
		"HAETAE_TEST_HOOKS=1",
		"HAETAE_AI_PROVIDER=local",
		"HAETAE_AI_BASE_URL="+h.prov.URL()+"/v1",
		"HAETAE_AI_MODEL=test",
	)
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
	setupCodeFile := filepath.Join(h.dataDir, "setup-code")
	waitFor(h.t, 10*time.Second, "setup-code file", func() bool {
		_, err := os.Stat(setupCodeFile)
		return err == nil
	})
}

func (h *harness) bootstrapAdmin() {
	h.t.Helper()
	codeBytes, err := os.ReadFile(filepath.Join(h.dataDir, "setup-code"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.admin = insecureClient(h.t)
	if status, body := h.api(h.admin, "POST", "/api/setup", map[string]string{
		"setupCode": strings.TrimSpace(string(codeBytes)), "orgName": "OperatorTest",
		"email": adminEmail, "password": adminPassword,
	}); status != http.StatusCreated {
		h.t.Fatalf("setup: %d %s", status, body)
	}
	if status, body := h.api(h.admin, "POST", "/api/login", map[string]string{
		"email": adminEmail, "password": adminPassword,
	}); status != 200 {
		h.t.Fatalf("admin login: %d %s", status, body)
	}
}

func (h *harness) enrollAndRunAgent() {
	h.t.Helper()
	var enr struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if status, body := h.api(h.admin, "POST", "/api/hosts/enrollment", map[string]any{
		"name": h.prefix + "-host", "ttlHours": 1,
	}, &enr); status != http.StatusCreated {
		h.t.Fatalf("enrollment: %d %s", status, body)
	}
	caPin := regexp.MustCompile(`--ca-pin '([^']+)'`).FindStringSubmatch(enr.Command)
	if caPin == nil {
		h.t.Fatalf("no CA pin in enrollment command:\n%s", enr.Command)
	}
	enroll := exec.Command(h.agentBin, "enroll",
		"--server", h.gwURL, "--token", enr.Token, "--ca-pin", caPin[1], "--state-dir", h.agentState)
	if out, err := enroll.CombinedOutput(); err != nil {
		h.t.Fatalf("agent enroll: %v\n%s", err, out)
	}
	h.startAgent()
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

func (h *harness) startAgent() {
	h.t.Helper()
	h.agentOut = &syncBuffer{}
	h.agentCmd = exec.Command(h.agentBin, "run", "--state-dir", h.agentState)
	h.agentCmd.Env = append(filterEnv(os.Environ(), aiEnvPrefixes...),
		"HAETAE_AGENT_HEALTH_WINDOW=2s",
		"HAETAE_AGENT_WATCH_INTERVAL=2s",
	)
	h.agentCmd.Stdout, h.agentCmd.Stderr = h.agentOut, h.agentOut
	h.agentCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := h.agentCmd.Start(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) tuneAlerts() {
	h.t.Helper()
	hour := (time.Now().UTC().Hour() + 12) % 24
	body := map[string]any{
		"timeZone": "UTC", "briefingHour": hour,
		"cpuPct": 90, "memPct": 90, "diskPct": 90,
		"sustainedS": 10, "offlineAfterS": 30,
	}
	if status, raw := h.api(h.admin, "PUT", "/api/settings/alerts", body); status != 200 {
		h.t.Fatalf("alert settings: %d %s", status, raw)
	}
}

// setOperator calls PUT /api/settings/operator.
func (h *harness) setOperator(enabled bool, autonomy string, cap int64) {
	h.t.Helper()
	if status, body := h.api(h.admin, "PUT", "/api/settings/operator", map[string]any{
		"aiEnabled": enabled, "autonomy": autonomy, "dailyTokenCap": cap,
	}); status != 200 {
		h.t.Fatalf("PUT operator settings: %d %s", status, body)
	}
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

// runContainer starts a prefixed container and returns its name and full id.
func (h *harness) runContainer(suffix string, args ...string) (name, id string) {
	h.t.Helper()
	name = h.prefix + "-" + suffix
	_ = exec.Command("docker", "rm", "-f", name).Run()
	out, err := exec.Command("docker", append([]string{"run", "-d", "--name", name}, args...)...).CombinedOutput()
	if err != nil {
		h.t.Fatalf("docker run %s: %v\n%s", name, err, out)
	}
	id = strings.TrimSpace(string(out))
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	})
	return name, id
}

// containerState returns the API state of a container ("" when unknown).
func (h *harness) containerState(id string) string {
	var out struct {
		Containers []map[string]any `json:"containers"`
	}
	if status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/containers", nil, &out); status != 200 {
		return ""
	}
	for _, c := range out.Containers {
		cid, _ := c["containerId"].(string)
		if cid == "" {
			cid, _ = c["id"].(string)
		}
		if cid != "" && (strings.HasPrefix(id, cid) || strings.HasPrefix(cid, id)) {
			s, _ := c["state"].(string)
			return strings.ToLower(s)
		}
	}
	return ""
}

func (h *harness) waitContainerState(id, state string, timeout time.Duration) {
	h.t.Helper()
	waitFor(h.t, timeout, "container "+shortCID(id)+" state "+state, func() bool {
		return h.containerState(id) == state
	})
}

// inspect returns one docker inspect Go-template value.
func inspect(t *testing.T, id, format string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", format, id).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", id, err)
	}
	return strings.TrimSpace(string(out))
}

type chatMsg struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Channel   string `json:"channel"`
	Direction string `json:"direction"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	Actions   []struct {
		ID string `json:"id"`
	} `json:"actions"`
	Evidence []struct {
		Label string `json:"label"`
		Path  string `json:"path"`
	} `json:"evidence"`
}

func (h *harness) listChat() []chatMsg {
	h.t.Helper()
	var out struct {
		Messages []chatMsg `json:"messages"`
	}
	if status, raw := h.api(h.admin, "GET", "/api/chat/messages?limit=200", nil, &out); status != 200 {
		h.t.Fatalf("chat messages: %d %s", status, raw)
	}
	return out.Messages
}

// waitChat waits for a web outbound message created after since that satisfies pred.
func (h *harness) waitChat(since time.Time, pred func(chatMsg) bool, timeout time.Duration) (chatMsg, time.Duration) {
	h.t.Helper()
	start := time.Now()
	var found chatMsg
	waitFor(h.t, timeout, "web chat message", func() bool {
		for _, m := range h.listChat() {
			if m.Direction != "out" || m.Channel != "web" || h.seenBeforeAsk[m.ID] {
				continue
			}
			ct, err := time.Parse(time.RFC3339Nano, m.CreatedAt)
			if err != nil || ct.Before(since.Add(-time.Second)) {
				continue
			}
			if pred(m) {
				found = m
				return true
			}
		}
		return false
	})
	return found, time.Since(start)
}

// ask posts a question as the admin. It returns the immediate reply ("" when
// the answer follows as a chat message).
func (h *harness) ask(q string) string {
	h.t.Helper()
	// Messages that exist now cannot be the answer to this question; the time
	// comparison alone accepts anything created up to a second earlier.
	h.seenBeforeAsk = map[string]bool{}
	for _, m := range h.listChat() {
		h.seenBeforeAsk[m.ID] = true
	}
	var out struct {
		Reply string `json:"reply"`
	}
	if status, raw := h.api(h.admin, "POST", "/api/chat/messages", map[string]string{"body": q}, &out); status != 200 {
		h.t.Fatalf("post question: %d %s", status, raw)
	}
	return out.Reply
}

// askAnswer asks and returns the reply text, whether immediate or a later answer message.
func (h *harness) askAnswer(q string, timeout time.Duration) (string, chatMsg, time.Duration) {
	h.t.Helper()
	since := time.Now()
	if reply := h.ask(q); reply != "" {
		return reply, chatMsg{Body: reply}, time.Since(since)
	}
	m, _ := h.waitChat(since, func(m chatMsg) bool { return m.Kind == "answer" && len(m.Actions) == 0 }, timeout)
	return m.Body, m, time.Since(since)
}

// dbActions returns (level, origin, outcome) rows of ai_actions for a container.
type actionRow struct{ Level, Origin, Outcome string }

func (h *harness) dbActions(containerID string) []actionRow {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := h.pool.Query(ctx,
		`SELECT level, origin, outcome FROM ai_actions WHERE container_id = $1 ORDER BY created_at`, containerID)
	if err != nil {
		h.t.Fatalf("query ai_actions: %v", err)
	}
	defer rows.Close()
	var res []actionRow
	for rows.Next() {
		var r actionRow
		if err := rows.Scan(&r.Level, &r.Origin, &r.Outcome); err != nil {
			h.t.Fatal(err)
		}
		res = append(res, r)
	}
	return res
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

func measure(t *testing.T, name string, d time.Duration) {
	t.Helper()
	t.Logf("MEASURE %s=%s", name, d.Round(time.Millisecond))
}

func shortCID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

var _ = fmt.Sprintf
