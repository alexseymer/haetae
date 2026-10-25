//go:build selfhost

package selfhost

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
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testEmail    = "admin@example.com"
	testPassword = "a long test passphrase"
	devImage     = "haetae-server:dev"
)

func TestComposeMatchesInstallScript(t *testing.T) {
	root := repoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(root, "deploy", "install-server.sh"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := extractHeredoc(script, "COMPOSE")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("deploy/compose.yaml differs from the heredoc in install-server.sh\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestInstallScriptShellcheck(t *testing.T) {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Skip("shellcheck not installed")
	}
	root := repoRoot(t)
	cmd := exec.Command("shellcheck", "-s", "sh", filepath.Join(root, "deploy", "install-server.sh"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck: %v\n%s", err, out)
	}
}

func TestSelfHostedInstall(t *testing.T) {
	skipWithoutDocker(t)
	root := repoRoot(t)
	ensureDevImage(t, root)

	dir := t.TempDir()
	webPort := freePort(t)
	gwPort := freePort(t)
	project := fmt.Sprintf("haetae-sh-%d", time.Now().UnixNano())

	t.Cleanup(func() {
		down := exec.Command("docker", "compose", "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml"), "down", "-v")
		down.Env = append(os.Environ(), "COMPOSE_PROJECT_NAME="+project)
		_ = down.Run()
	})

	script := filepath.Join(root, "deploy", "install-server.sh")
	start := time.Now()
	out := runInstall(t, script, dir, project, webPort, gwPort)
	url, code := parseInstallOutput(t, out)
	if url != fmt.Sprintf("https://127.0.0.1:%s", webPort) {
		t.Fatalf("URL = %q", url)
	}
	if code == "" {
		t.Fatal("no setup code in install output")
	}

	c := insecureClient(t)
	setupBody := map[string]string{
		"setupCode": code, "orgName": "Acme", "email": testEmail, "password": testPassword,
	}
	if status, _, body := apiDo(t, c, http.MethodPost, url+"/api/setup", setupBody); status != http.StatusCreated {
		t.Fatalf("setup: %d %s", status, body)
	}
	if status, _, body := apiDo(t, c, http.MethodGet, url+"/api/me", nil); status != 200 {
		t.Fatalf("/api/me after setup: %d %s", status, body)
	}

	c2 := insecureClient(t)
	if status, _, body := apiDo(t, c2, http.MethodPost, url+"/api/login", map[string]string{
		"email": testEmail, "password": testPassword,
	}); status != 200 {
		t.Fatalf("login: %d %s", status, body)
	}

	elapsed := time.Since(start)
	t.Logf("MEASURE install-to-login: %s (limit 5m0s)", elapsed.Round(time.Millisecond))
	if elapsed >= 5*time.Minute {
		t.Fatalf("install-to-login took %s, want < 5m", elapsed)
	}

	assertDownloads(t, c2, url, root)

	logs := composeOutput(t, dir, project, "logs")
	if strings.Contains(logs, "level=ERROR") {
		t.Fatalf("compose logs contain level=ERROR:\n%s", logs)
	}

	envBefore, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	out2 := runInstall(t, script, dir, project, webPort, gwPort)
	if !strings.Contains(out2, url) {
		t.Fatalf("re-run output missing URL:\n%s", out2)
	}
	envAfter, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(envBefore, envAfter) {
		t.Fatalf(".env changed on re-run\nbefore:\n%s\nafter:\n%s", envBefore, envAfter)
	}
	// Data survived: login still works.
	c3 := insecureClient(t)
	if status, _, body := apiDo(t, c3, http.MethodPost, url+"/api/login", map[string]string{
		"email": testEmail, "password": testPassword,
	}); status != 200 {
		t.Fatalf("login after re-run: %d %s", status, body)
	}
}

func TestServerWorksWithoutInternetAccess(t *testing.T) {
	skipWithoutDocker(t)
	root := repoRoot(t)
	ensureDevImage(t, root)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	netName := "haetae-iso-" + suffix
	dbName := "haetae-iso-db-" + suffix
	srvName := "haetae-iso-srv-" + suffix
	pw := "testpassword" + suffix[:8]

	run(t, "docker", "network", "create", "--internal", netName)
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", srvName).Run()
		_ = exec.Command("docker", "rm", "-f", dbName).Run()
		_ = exec.Command("docker", "network", "rm", netName).Run()
	})

	run(t, "docker", "run", "-d", "--name", dbName, "--network", netName,
		"-e", "POSTGRES_PASSWORD="+pw, "-e", "POSTGRES_USER=haetae", "-e", "POSTGRES_DB=haetae",
		"postgres:17-alpine")
	waitPostgres(t, dbName)

	dbURL := fmt.Sprintf("postgres://haetae:%s@%s:5432/haetae?sslmode=disable", pw, dbName)
	run(t, "docker", "run", "-d", "--name", srvName, "--network", netName,
		"-e", "HAETAE_DATABASE_URL="+dbURL,
		"-e", "HAETAE_WEB_ADDR=:8080",
		"-e", "HAETAE_GATEWAY_ADDR=:8443",
		"-e", "HAETAE_WEB_URL=https://"+srvName+":8080",
		"-e", "HAETAE_GATEWAY_URL=https://"+srvName+":8443",
		"-e", "HAETAE_GATEWAY_NAMES="+srvName,
		devImage)

	// Wait for /healthz from another container on the same internal network.
	run(t, "docker", "run", "--rm", "--user", "root", "--network", netName, "curlimages/curl:8.11.1",
		"sh", "-c",
		`i=0; while [ $i -lt 90 ]; do curl -fsSk "https://`+srvName+`:8080/healthz" && exit 0; i=$((i+1)); sleep 1; done; exit 1`)

	codeOut := run(t, "docker", "exec", srvName, "/usr/local/bin/haetae-server", "setup-code")
	code := strings.TrimSpace(codeOut)
	if code == "" {
		t.Fatal("empty setup code")
	}

	base := "https://" + srvName + ":8080"
	// Drive the API from a curl container (no published ports).
	setupJSON := fmt.Sprintf(`{"setupCode":%q,"orgName":"Iso","email":%q,"password":%q}`, code, testEmail, testPassword)
	cookieFile := "/tmp/haetae-cookies"
	run(t, "docker", "run", "--rm", "--user", "root", "--network", netName, "curlimages/curl:8.11.1",
		"sh", "-c",
		`curl -fsSk -c `+cookieFile+` -H 'Content-Type: application/json' -H 'X-Haetae-Request: 1' `+
			`-d `+shellQuote(setupJSON)+` `+shellQuote(base+"/api/setup")+
			` && curl -fsSk -b `+cookieFile+` -c `+cookieFile+` -H 'Content-Type: application/json' -H 'X-Haetae-Request: 1' `+
			`-d `+shellQuote(fmt.Sprintf(`{"email":%q,"password":%q}`, testEmail, testPassword))+` `+shellQuote(base+"/api/login")+
			` && curl -fsSk -b `+cookieFile+` -H 'Content-Type: application/json' -H 'X-Haetae-Request: 1' `+
			`-d '{"name":"iso-host","ttlHours":1}' `+shellQuote(base+"/api/hosts/enrollment")+
			` && curl -fsSk -o /dev/null -w '%{http_code}\n' `+shellQuote(base+"/install.sh")+` | grep -q 200`)

	inspect := run(t, "docker", "network", "inspect", netName)
	if !strings.Contains(inspect, `"Internal": true`) && !strings.Contains(inspect, `"Internal":true`) {
		t.Fatalf("network is not internal: %s", inspect)
	}
	nets := run(t, "docker", "inspect", "-f", `{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}`, srvName)
	names := strings.Fields(nets)
	if len(names) != 1 || names[0] != netName {
		t.Fatalf("server networks = %q, want only %s", nets, netName)
	}
}

var (
	imageOnce   sync.Once
	imageErr    error
	imageBuilt  time.Duration
	imageLogged sync.Once
)

func ensureDevImage(t *testing.T, root string) {
	t.Helper()
	imageOnce.Do(func() {
		start := time.Now()
		cmd := exec.Command("docker", "build", "--build-arg", "VERSION=dev", "-t", devImage, root)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// Classic builder: this host's Docker enables BuildKit without the buildx plugin.
		cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=0")
		imageErr = cmd.Run()
		imageBuilt = time.Since(start)
	})
	if imageErr != nil {
		t.Fatalf("docker build: %v", imageErr)
	}
	imageLogged.Do(func() {
		t.Logf("MEASURE build %s", imageBuilt.Round(time.Millisecond))
	})
}

func skipWithoutDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker unavailable: " + err.Error())
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose unavailable: " + err.Error())
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
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

func runInstall(t *testing.T, script, dir, project, webPort, gwPort string) string {
	t.Helper()
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(),
		"HAETAE_DIR="+dir,
		"HAETAE_IMAGE="+devImage,
		"HAETAE_NO_PULL=1",
		"HAETAE_HOST=127.0.0.1",
		"HAETAE_WEB_PORT="+webPort,
		"HAETAE_GATEWAY_PORT="+gwPort,
		"COMPOSE_PROJECT_NAME="+project,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install-server.sh: %v\n%s", err, out)
	}
	return string(out)
}

func parseInstallOutput(t *testing.T, out string) (url, code string) {
	t.Helper()
	urlRe := regexp.MustCompile(`(?m)^\s*URL:\s+(\S+)\s*$`)
	codeRe := regexp.MustCompile(`(?m)^\s*Setup code:\s+(\S+)\s*$`)
	if m := urlRe.FindStringSubmatch(out); m != nil {
		url = m[1]
	}
	if m := codeRe.FindStringSubmatch(out); m != nil {
		code = m[1]
	}
	if url == "" || code == "" {
		t.Fatalf("could not parse URL/setup code from:\n%s", out)
	}
	return url, code
}

func insecureClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test: self-signed server CA
		},
		Timeout: 30 * time.Second,
	}
}

func apiDo(t *testing.T, c *http.Client, method, rawURL string, body any) (int, http.Header, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, rawURL, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Haetae-Request", "1")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func assertDownloads(t *testing.T, c *http.Client, base, root string) {
	t.Helper()
	if status, _, _ := apiDo(t, c, http.MethodGet, base+"/install.sh", nil); status != 200 {
		t.Fatalf("/install.sh = %d", status)
	}
	status, _, sums := apiDo(t, c, http.MethodGet, base+"/download/SHA256SUMS", nil)
	if status != 200 {
		t.Fatalf("/download/SHA256SUMS = %d", status)
	}
	status, _, bin := apiDo(t, c, http.MethodGet, base+"/download/haetae-agent-linux-amd64", nil)
	if status != 200 {
		t.Fatalf("/download/haetae-agent-linux-amd64 = %d", status)
	}
	sum := sha256.Sum256(bin)
	want := hex.EncodeToString(sum[:])
	found := false
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "haetae-agent-linux-amd64" {
			found = true
			if !strings.EqualFold(fields[0], want) {
				t.Fatalf("checksum mismatch: sums=%s body=%s", fields[0], want)
			}
		}
	}
	if !found {
		t.Fatalf("SHA256SUMS missing amd64 line:\n%s", sums)
	}
	_ = root
}

func composeOutput(t *testing.T, dir, project string, args ...string) string {
	t.Helper()
	a := append([]string{"compose", "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml")}, args...)
	cmd := exec.Command("docker", a...)
	cmd.Env = append(os.Environ(), "COMPOSE_PROJECT_NAME="+project)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func waitPostgres(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command("docker", "exec", name, "pg_isready", "-U", "haetae", "-d", "haetae")
		if err := cmd.Run(); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("postgres not ready")
}

func extractHeredoc(script []byte, tag string) ([]byte, error) {
	start := []byte("<<" + "'" + tag + "'\n")
	i := bytes.Index(script, start)
	if i < 0 {
		start = []byte("<<" + tag + "\n")
		i = bytes.Index(script, start)
		if i < 0 {
			return nil, fmt.Errorf("heredoc %s not found", tag)
		}
	}
	i += len(start)
	end := []byte("\n" + tag + "\n")
	j := bytes.Index(script[i:], end)
	if j < 0 {
		// final marker may be at EOF without trailing newline after tag
		end2 := []byte("\n" + tag)
		j = bytes.Index(script[i:], end2)
		if j < 0 {
			return nil, fmt.Errorf("heredoc %s end not found", tag)
		}
		return script[i : i+j], nil
	}
	return append(script[i:i+j], '\n'), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
