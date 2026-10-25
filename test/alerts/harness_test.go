//go:build e2e_alerts

package alerts_test

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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	adminEmail     = "admin@htest.example"
	adminPassword  = "a long test passphrase"
	memberEmail    = "member@htest.example"
	memberPassword = "another long passphrase"
	org2Email      = "admin@htest-org2.example"
	org2Password   = "org two long passphrase"
	tgToken        = "htest-telegram-token"
	adminTgID      = int64(424201)
	memberTgID     = int64(424202)
	unlinkedTgID   = int64(999001)
)

// harness is one live server + enrolled agent + fake Telegram shared by scenarios.
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
	webPort    string
	gwPort     string
	hostID     string
	prefix     string
	serverCmd  *exec.Cmd
	agentCmd   *exec.Cmd
	serverOut  *syncBuffer
	agentOut   *syncBuffer
	admin      *http.Client
	member     *http.Client
	org2       *http.Client
	tg         *fakeTelegram
	extraEnv   []string
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
		tg:         startFakeTelegram(t),
	}
	_, h.dbURL = storetest.NewWithURL(t)

	h.webPort, h.gwPort = freePort(t), freePort(t)
	h.webURL = "https://127.0.0.1:" + h.webPort
	h.gwURL = "https://127.0.0.1:" + h.gwPort

	h.startServer(nil, true)
	h.bootstrapAccounts()
	h.enrollAndRunAgent()
	h.tuneSettings()
	h.linkTelegram(h.admin, adminTgID)

	t.Cleanup(func() {
		killProcessGroup(h.agentCmd)
		killProcessGroup(h.serverCmd)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = dockerclean.ForceRemove(h.prefix)
		_ = ctx
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
		"HAETAE_TELEGRAM_TOKEN="+tgToken,
		"HAETAE_TELEGRAM_BASE_URL="+h.tg.URL(),
	)
	env = append(env, extra...)
	h.extraEnv = append([]string(nil), extra...)
	return env
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
		// Also drop any leftover briefing force from the parent shell.
		if key == "HAETAE_BRIEFING_NOW" {
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

func (h *harness) restartServer(extra []string) {
	h.t.Helper()
	killProcessGroup(h.serverCmd)
	h.startServer(extra, false)
	// Sessions persist in DB; cookies usually still work. Re-login if needed.
	if status, body := h.api(h.admin, "GET", "/api/me", nil); status != 200 {
		if status, body = h.api(h.admin, "POST", "/api/login", map[string]string{
			"email": adminEmail, "password": adminPassword,
		}); status != 200 {
			h.t.Fatalf("re-login after restart: %d %s", status, body)
		}
	}
	if h.agentCmd != nil && h.agentCmd.Process != nil {
		waitFor(h.t, 45*time.Second, "host reconnected", func() bool {
			var hosts struct {
				Hosts []struct {
					Connected bool `json:"connected"`
				} `json:"hosts"`
			}
			status, _ := h.api(h.admin, "GET", "/api/hosts", nil, &hosts)
			return status == 200 && len(hosts.Hosts) == 1 && hosts.Hosts[0].Connected
		})
	}
}

func (h *harness) bootstrapAccounts() {
	h.t.Helper()
	codeBytes, err := os.ReadFile(filepath.Join(h.dataDir, "setup-code"))
	if err != nil {
		h.t.Fatal(err)
	}
	code := strings.TrimSpace(string(codeBytes))

	h.admin = insecureClient(h.t)
	if status, body := h.api(h.admin, "POST", "/api/setup", map[string]string{
		"setupCode": code, "orgName": "AlertsTest", "email": adminEmail, "password": adminPassword,
	}); status != http.StatusCreated {
		h.t.Fatalf("setup: %d %s", status, body)
	}
	if status, body := h.api(h.admin, "POST", "/api/login", map[string]string{
		"email": adminEmail, "password": adminPassword,
	}); status != 200 {
		h.t.Fatalf("admin login: %d %s", status, body)
	}
	if status, body := h.api(h.admin, "POST", "/api/users", map[string]string{
		"email": memberEmail, "password": memberPassword, "role": "member",
	}); status != http.StatusCreated {
		h.t.Fatalf("create member: %d %s", status, body)
	}
	h.member = insecureClient(h.t)
	if status, body := h.api(h.member, "POST", "/api/login", map[string]string{
		"email": memberEmail, "password": memberPassword,
	}); status != 200 {
		h.t.Fatalf("member login: %d %s", status, body)
	}

	h.seedSecondOrg()
}

func (h *harness) seedSecondOrg() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, h.dbURL)
	if err != nil {
		h.t.Fatal(err)
	}
	defer pool.Close()

	var orgID string
	if err := pool.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id`, "AlertsOrg2").Scan(&orgID); err != nil {
		h.t.Fatalf("create org2: %v", err)
	}
	hash, err := auth.HashPassword(org2Password)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (org_id, email, password_hash, role) VALUES ($1, $2, $3, 'admin')`,
		orgID, org2Email, hash,
	); err != nil {
		h.t.Fatalf("create org2 user: %v", err)
	}
	h.org2 = insecureClient(h.t)
	if status, body := h.api(h.org2, "POST", "/api/login", map[string]string{
		"email": org2Email, "password": org2Password,
	}); status != 200 {
		h.t.Fatalf("org2 login: %d %s", status, body)
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
		"--server", h.gwURL,
		"--token", enr.Token,
		"--ca-pin", caPin[1],
		"--state-dir", h.agentState,
	)
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
		if status != 200 || len(hosts.Hosts) != 1 {
			return false
		}
		if hosts.Hosts[0].Connected {
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

func (h *harness) stopAgent() {
	h.t.Helper()
	killProcessGroup(h.agentCmd)
	h.agentCmd = nil
}

func (h *harness) tuneSettings() {
	h.t.Helper()
	body := map[string]any{
		"timeZone": "UTC", "briefingHour": time.Now().UTC().Hour(),
		"cpuPct": 90, "memPct": 90, "diskPct": 90,
		"sustainedS": 10, "offlineAfterS": 30,
	}
	if status, raw := h.api(h.admin, "PUT", "/api/settings/alerts", body); status != 200 {
		h.t.Fatalf("settings: %d %s", status, raw)
	}
}

func (h *harness) linkTelegram(c *http.Client, tgID int64) {
	h.t.Helper()
	var link struct {
		Code string `json:"code"`
	}
	if status, body := h.api(c, "POST", "/api/chat/link", map[string]any{"channel": "telegram"}, &link); status != 200 {
		h.t.Fatalf("chat link: %d %s", status, body)
	}
	if link.Code == "" {
		h.t.Fatal("empty link code")
	}
	h.tg.PushMessage(tgID, "/link "+link.Code)
	waitFor(h.t, 20*time.Second, "telegram linked", func() bool {
		var out struct {
			Accounts []map[string]any `json:"accounts"`
		}
		status, _ := h.api(c, "GET", "/api/chat/accounts", nil, &out)
		return status == 200 && len(out.Accounts) >= 1
	})
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

func (h *harness) hostLastSeen() time.Time {
	h.t.Helper()
	var hosts struct {
		Hosts []struct {
			ID       string  `json:"id"`
			LastSeen *string `json:"lastSeen"`
		} `json:"hosts"`
	}
	status, raw := h.api(h.admin, "GET", "/api/hosts", nil, &hosts)
	if status != 200 || len(hosts.Hosts) == 0 {
		h.t.Fatalf("hosts: %d %s", status, raw)
	}
	ls := hosts.Hosts[0].LastSeen
	if ls == nil || *ls == "" {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339Nano, *ls)
	if err != nil {
		t, err = time.Parse(time.RFC3339, *ls)
	}
	if err != nil {
		h.t.Fatalf("parse lastSeen %q: %v", *ls, err)
	}
	return t
}

func (h *harness) waitIncidentRule(rule string, state string, timeout time.Duration) (map[string]any, time.Duration) {
	h.t.Helper()
	start := time.Now()
	var found map[string]any
	waitFor(h.t, timeout, fmt.Sprintf("incident/alert %s %s", rule, state), func() bool {
		var out struct {
			Alerts []map[string]any `json:"alerts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/alerts?state="+state, nil, &out)
		if status != 200 {
			return false
		}
		for _, a := range out.Alerts {
			if a["rule"] == rule && a["hostId"] == h.hostID {
				found = a
				return true
			}
		}
		return false
	})
	return found, time.Since(start)
}

func (h *harness) waitWebMessage(since time.Time, pred func(map[string]any) bool, timeout time.Duration) (map[string]any, time.Duration) {
	h.t.Helper()
	start := time.Now()
	var found map[string]any
	waitFor(h.t, timeout, "web chat message", func() bool {
		var out struct {
			Messages []map[string]any `json:"messages"`
		}
		status, _ := h.api(h.admin, "GET", "/api/chat/messages?limit=100", nil, &out)
		if status != 200 {
			return false
		}
		for _, m := range out.Messages {
			created, _ := m["createdAt"].(string)
			if created != "" {
				ct, err := time.Parse(time.RFC3339Nano, created)
				if err != nil {
					ct, _ = time.Parse(time.RFC3339, created)
				}
				if !ct.IsZero() && ct.Before(since.Add(-time.Second)) {
					continue
				}
			}
			if pred == nil || pred(m) {
				found = m
				return true
			}
		}
		return false
	})
	return found, time.Since(start)
}

func (h *harness) countWebMessages(pred func(map[string]any) bool) int {
	h.t.Helper()
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	status, raw := h.api(h.admin, "GET", "/api/chat/messages?limit=200", nil, &out)
	if status != 200 {
		h.t.Fatalf("messages: %d %s", status, raw)
	}
	n := 0
	for _, m := range out.Messages {
		if pred == nil || pred(m) {
			n++
		}
	}
	return n
}

func (h *harness) waitTelegram(chatID int64, since time.Time, pred func(tgSent) bool, timeout time.Duration) (tgSent, time.Duration) {
	h.t.Helper()
	start := time.Now()
	var found tgSent
	waitFor(h.t, timeout, "telegram sendMessage", func() bool {
		for _, m := range h.tg.Messages() {
			if m.ChatID != chatID || m.At.Before(since.Add(-time.Second)) {
				continue
			}
			if pred == nil || pred(m) {
				found = m
				return true
			}
		}
		return false
	})
	return found, time.Since(start)
}

func (h *harness) listMutes() []map[string]any {
	h.t.Helper()
	var out struct {
		Mutes []map[string]any `json:"mutes"`
	}
	status, raw := h.api(h.admin, "GET", "/api/mutes", nil, &out)
	if status != 200 {
		h.t.Fatalf("mutes: %d %s", status, raw)
	}
	return out.Mutes
}

func (h *harness) assertNoAIEnv() {
	h.t.Helper()
	for _, e := range h.serverCmd.Env {
		key, _, _ := strings.Cut(e, "=")
		for _, p := range aiEnvPrefixes {
			if strings.HasPrefix(key, p) {
				h.t.Fatalf("server env contains AI var %s", key)
			}
		}
	}
}

func (h *harness) assertLocalPeersOnly() {
	h.t.Helper()
	if h.serverCmd == nil || h.serverCmd.Process == nil {
		h.t.Fatal("no server pid")
	}
	pid := h.serverCmd.Process.Pid
	out, err := exec.Command("ss", "-tpn").CombinedOutput()
	if err != nil {
		h.t.Fatalf("ss: %v\n%s", err, out)
	}
	pidMark := "pid=" + strconv.Itoa(pid)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, pidMark) {
			continue
		}
		// Skip listen sockets.
		if strings.HasPrefix(strings.TrimSpace(line), "LISTEN") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		peer := fields[4]
		host, _, err := net.SplitHostPort(peer)
		if err != nil {
			host = peer
		}
		if host == "127.0.0.1" || host == "::1" || host == "*" || host == "0.0.0.0" {
			continue
		}
		h.t.Fatalf("server pid %d has non-local peer %q in: %s", pid, peer, line)
	}
}

func (h *harness) runCrashLoopContainer() string {
	h.t.Helper()
	name := h.prefix + "-crash"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	cmd := exec.Command("docker", "run", "-d", "--name", name,
		"--restart", "always",
		"busybox:1.37", "sh", "-c", "exit 1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("docker run crash: %v\n%s", err, out)
	}
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	})
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
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test: internal CA
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
		time.Sleep(200 * time.Millisecond)
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
