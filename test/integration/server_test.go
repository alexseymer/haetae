//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

// serverProc is the real haetae-server binary running against a test database.
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

// insecureClient does not verify the server certificate: the test checks the
// pin itself where it matters.
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

// The real server: first-run setup with the one-time code, a correct pin in
// the install command, and an agent enrolled with a token minted through the
// API that then shows up connected in the API.
func TestServerSetupAndEnrollThroughTheAPI(t *testing.T) {
	p := startServer(t)
	c := p.insecureClient()

	var status struct {
		SetupRequired bool `json:"setupRequired"`
	}
	if p.call(c, "GET", "/api/status", nil, &status) != 200 || !status.SetupRequired {
		t.Fatalf("status = %+v", status)
	}

	// The setup code exists only on the server: in its data directory and its log.
	raw, err := os.ReadFile(filepath.Join(p.dataDir, "setup-code"))
	if err != nil {
		t.Fatalf("no setup-code file: %v", err)
	}
	code := strings.TrimSpace(string(raw))
	if info, _ := os.Stat(filepath.Join(p.dataDir, "setup-code")); info.Mode().Perm() != 0o600 {
		t.Errorf("setup-code mode = %v, want 0600", info.Mode().Perm())
	}
	if !strings.Contains(p.out.String(), code) {
		t.Error("the setup code was not logged for the operator")
	}
	if s := p.call(c, "POST", "/api/setup", map[string]string{"setupCode": "wrong", "email": "a@example.com", "password": "a long test passphrase"}, nil); s != http.StatusForbidden {
		t.Fatalf("wrong code = %d", s)
	}
	if s := p.call(c, "POST", "/api/setup", map[string]string{"setupCode": code, "orgName": "Acme", "email": "admin@example.com", "password": "a long test passphrase"}, nil); s != http.StatusCreated {
		t.Fatalf("setup = %d", s)
	}
	if _, err := os.Stat(filepath.Join(p.dataDir, "setup-code")); !os.IsNotExist(err) {
		t.Error("the setup code file was not removed after setup")
	}

	// Mint a token through the API.
	var enr struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if s := p.call(c, "POST", "/api/hosts/enrollment", map[string]any{"name": "e2e-host", "ttlHours": 1}, &enr); s != http.StatusCreated {
		t.Fatalf("enrollment = %d", s)
	}

	// The web pin in the command is the pin of the certificate the server really presents.
	conn, err := tls.Dial("tcp", strings.TrimPrefix(p.webURL, "https://"), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test: reading the certificate
	if err != nil {
		t.Fatal(err)
	}
	spki := sha256.Sum256(conn.ConnectionState().PeerCertificates[0].RawSubjectPublicKeyInfo)
	conn.Close()
	wantPin := "sha256//" + base64.StdEncoding.EncodeToString(spki[:])
	if !strings.Contains(enr.Command, "--pinnedpubkey '"+wantPin+"'") {
		t.Fatalf("command does not carry the real web pin %s:\n%s", wantPin, enr.Command)
	}
	caPin := regexp.MustCompile(`--ca-pin '([^']+)'`).FindStringSubmatch(enr.Command)
	if caPin == nil {
		t.Fatalf("no CA pin in command: %s", enr.Command)
	}

	// Enroll a real agent with what the command contains and run it.
	id, err := agent.Enroll(context.Background(), p.gwURL, enr.Token, caPin[1], t.TempDir())
	if err != nil {
		t.Fatalf("enroll with the API's token: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = agent.Run(ctx, id, nilLogger(), agent.RunOptions{}) }()

	type hostJSON struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
	}
	var hosts struct{ Hosts []hostJSON }
	waitFor(t, 30*time.Second, "enrolled host connected in the API", func() bool {
		hosts.Hosts = nil
		p.call(c, "GET", "/api/hosts", nil, &hosts)
		return len(hosts.Hosts) == 1 && hosts.Hosts[0].Connected
	})
	if hosts.Hosts[0].Name != "e2e-host" || hosts.Hosts[0].ID != id.HostID {
		t.Fatalf("host = %+v, want e2e-host / %s", hosts.Hosts[0], id.HostID)
	}

	// The token is single-use.
	if _, err := agent.Enroll(context.Background(), p.gwURL, enr.Token, caPin[1], t.TempDir()); err == nil {
		t.Fatal("the enrollment token worked twice")
	}
}

func TestServerRefusesHalfConfiguredTLS(t *testing.T) {
	_, dbURL := storetest.NewWithURL(t)
	cmd := exec.Command(buildServer(t), "serve")
	cmd.Env = append(os.Environ(), "HAETAE_DATABASE_URL="+dbURL, "HAETAE_DATA_DIR="+t.TempDir(), "HAETAE_WEB_TLS_CERT=/nonexistent.crt")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "must be set together") {
		t.Fatalf("server started with only HAETAE_WEB_TLS_CERT: err=%v out=%s", err, out)
	}
}

func nilLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// syncBuffer is a bytes.Buffer that is safe to read while a process writes to it.
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
