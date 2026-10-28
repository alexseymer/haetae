//go:build screenshots

package screenshots_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

// metricsWarmup is how long the demo runs before shooting: the agent samples
// every 10 s, so this gives the charts a few minutes of real points.
const metricsWarmup = 4 * time.Minute

// TestScreenshots starts a real server and agent with a believable demo
// (four containers, a crash loop and its incident, chat, findings) and drives
// Chromium over it with shoot.cjs, writing docs/images/*.png.
func TestScreenshots(t *testing.T) {
	ensureImage(t, "postgres:17-alpine")
	removeSharedTestDB(t)
	// shop-db doubles as the control plane's PostgreSQL, so the host shows
	// exactly the four demo containers and no test database container.
	dbPort := startDemoDB(t)
	t.Setenv("HAETAE_TEST_DATABASE_URL", fmt.Sprintf(
		"postgres://postgres:%s@127.0.0.1:%s/postgres?sslmode=disable", demoDBPassword, dbPort))
	h := startHarness(t)
	ensureWebBuilt(t, h.repoRoot)
	outDir := filepath.Join(h.repoRoot, "docs", "images")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	h.seedAlertSettings()
	h.seedAdvisories()
	h.prov.setScript(operatorScript(h.hostID))
	h.putOperator(true, "suggest", 200_000)

	start := time.Now()
	h.startDemoContainers()
	t.Logf("demo containers started; waiting for the incident")

	waitFor(t, 3*time.Minute, "incident chat message", func() bool {
		for _, m := range h.listChat() {
			if m.Kind == "incident" {
				return true
			}
		}
		return false
	})
	waitFor(t, 2*time.Minute, "worker logs collected by the agent", func() bool {
		_, raw := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/logs?limit=200", nil)
		return strings.Contains(raw, "connection refused")
	})
	h.askChat(t, "Which containers are running on hel1-prod?")
	h.waitWorkerMidRun(t)
	h.askChat(t, "Why does the worker keep restarting?")

	nudged := map[string]time.Time{}
	waitFor(t, 4*time.Minute, "scans, CVE findings and misconfigurations", func() bool { return h.securityReady(nudged) })

	if wait := metricsWarmup - time.Since(start); wait > 0 {
		t.Logf("waiting %s for metric samples", wait.Round(time.Second))
		time.Sleep(wait)
	}

	// Another test run may have recreated the shared test container meanwhile.
	removeSharedTestDB(t)
	waitFor(t, time.Minute, "agent to drop the removed container", func() bool {
		var out struct {
			Containers []struct {
				Name string `json:"name"`
			} `json:"containers"`
		}
		if s, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/containers", nil, &out); s != 200 {
			return false
		}
		for _, c := range out.Containers {
			if c.Name == "haetae-test-postgres" {
				return false
			}
		}
		return len(out.Containers) == 4
	})

	// Development aid: SHOT_HOLD=<file> writes the connection details there and
	// keeps the stack up until the file is deleted, so shoot.cjs can be re-run.
	if hold := os.Getenv("SHOT_HOLD"); hold != "" {
		info := fmt.Sprintf("SHOT_WEB_URL=%s SHOT_EMAIL=%s SHOT_PASSWORD='%s' SHOT_HOST_ID=%s SHOT_OUT_DIR=%s\n",
			h.webURL, adminEmail, adminPassword, h.hostID, outDir)
		_ = os.WriteFile(hold, []byte(info), 0o600)
		for {
			if _, err := os.Stat(hold); err != nil {
				break
			}
			time.Sleep(time.Second)
		}
	}

	node := exec.Command("node", filepath.Join(h.repoRoot, "test", "screenshots", "shoot.cjs"))
	node.Dir = h.repoRoot
	node.Env = append(os.Environ(),
		"SHOT_WEB_URL="+h.webURL,
		"SHOT_EMAIL="+adminEmail,
		"SHOT_PASSWORD="+adminPassword,
		"SHOT_HOST_ID="+h.hostID,
		"SHOT_OUT_DIR="+outDir,
		"NODE_PATH="+npmRootGlobal(t),
	)
	node.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &syncBuffer{}
	node.Stdout, node.Stderr = out, out
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	defer killProcessGroup(node)
	err := node.Wait()
	t.Logf("shoot.cjs output:\n%s", out.String())
	if err != nil {
		t.Fatalf("shoot.cjs failed: %v", err)
	}
}

// waitWorkerMidRun waits until the worker has been up for a few seconds and
// still has most of its 20 s run ahead: the agent only answers log queries
// for containers it is currently following.
func (h *harness) waitWorkerMidRun(t *testing.T) {
	t.Helper()
	waitFor(t, 3*time.Minute, "worker mid-run", func() bool {
		out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}} {{.State.StartedAt}}", "worker").Output()
		if err != nil {
			return false
		}
		f := strings.Fields(string(out))
		if len(f) != 2 || f[0] != "true" {
			return false
		}
		started, err := time.Parse(time.RFC3339Nano, f[1])
		if err != nil {
			return false
		}
		age := time.Since(started)
		return age > 4*time.Second && age < 8*time.Second
	})
}

func (h *harness) seedAlertSettings() {
	h.t.Helper()
	hour := (time.Now().UTC().Hour() + 12) % 24 // keep the daily briefing out of the demo chat
	if status, raw := h.api(h.admin, "PUT", "/api/settings/alerts", map[string]any{
		"timeZone": "UTC", "briefingHour": hour,
		"cpuPct": 90, "memPct": 90, "diskPct": 90,
		"sustainedS": 300, "offlineAfterS": 120,
	}); status != 200 {
		h.t.Fatalf("alert settings: %d %s", status, raw)
	}
}

func (h *harness) putOperator(enabled bool, autonomy string, cap int64) {
	h.t.Helper()
	if status, body := h.api(h.admin, "PUT", "/api/settings/operator", map[string]any{
		"aiEnabled": enabled, "autonomy": autonomy, "dailyTokenCap": cap,
	}); status != 200 {
		h.t.Fatalf("PUT operator settings: %d %s", status, body)
	}
}

const demoDBPassword = "change-me-in-prod"

// startDemoDB starts shop-db (PostgreSQL, published on a host port like a
// careless real deployment) and returns the host port once it accepts queries.
func startDemoDB(t *testing.T) string {
	t.Helper()
	port := freePort(t)
	runDemoContainer(t, "shop-db",
		"--label", "com.docker.compose.project=shop", "--label", "com.docker.compose.service=db",
		"-e", "POSTGRES_PASSWORD="+demoDBPassword, "-e", "POSTGRES_DB=shop",
		"-p", "0.0.0.0:"+port+":5432", "postgres:17-alpine")
	ok := 0
	waitFor(t, 60*time.Second, "shop-db ready", func() bool {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
		if err == nil {
			_ = conn.Close()
		}
		if err == nil && exec.Command("docker", "exec", "shop-db", "pg_isready", "-U", "postgres", "-h", "127.0.0.1").Run() == nil {
			ok++
		} else {
			ok = 0
		}
		return ok >= 8 // the image restarts the server once after initialising
	})
	return port
}

// startDemoContainers starts the other three demo containers of a small shop.
func (h *harness) startDemoContainers() {
	compose := func(service string) []string {
		return []string{
			"--label", "com.docker.compose.project=shop",
			"--label", "com.docker.compose.service=" + service,
		}
	}
	run := func(name, service string, args ...string) {
		h.runContainer(name, append(compose(service), args...)...)
	}
	run("shop-web", "web", "-p", "0.0.0.0:"+freePort(h.t)+":80", "nginx:alpine")
	apiScript := `
paths="/api/products /api/cart /api/orders /api/products/42 /healthz"
n=0
while true; do
  n=$((n+1))
  set -- $paths
  shift $((n % 5)) 2>/dev/null
  p=${1:-/api/products}
  ms=$((8 + RANDOM % 60))
  lvl=info; st=200
  if [ $((n % 37)) -eq 0 ]; then lvl=warn; st=429; fi
  echo "{\"ts\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"level\":\"$lvl\",\"msg\":\"request completed\",\"method\":\"GET\",\"path\":\"$p\",\"status\":$st,\"duration_ms\":$ms}"
  if [ $((n % 15)) -eq 0 ]; then i=0; while [ $i -lt 15000 ]; do i=$((i+1)); done; fi
  sleep 1
done`
	run("shop-api", "api", "busybox:1.37", "sh", "-c", apiScript)
	workerScript := `
echo '{"level":"info","msg":"worker starting","queue":"orders","concurrency":4}'
sleep 20
echo '{"level":"error","msg":"connect to redis:6379 failed: connection refused","queue":"orders"}'
echo '{"level":"fatal","msg":"cannot start without a queue backend, exiting"}'
exit 1`
	run("worker", "worker", "--restart", "always", "busybox:1.37", "sh", "-c", workerScript)
}

// seedAdvisories publishes a crafted Grype database (synthetic CVEs) matching
// real packages of the nginx:alpine image, at plausible severities.
func (h *harness) seedAdvisories() {
	h.t.Helper()
	doc := runInventory(h.t, h.inventoryBin, "nginx:alpine")
	have := map[string]string{}
	for _, p := range doc.Packages {
		if p.Type == "apk" {
			have[p.Name] = p.Version
		}
	}
	parts := strings.SplitN(doc.Distro.Version, ".", 3)
	if len(parts) < 2 {
		h.t.Fatalf("unexpected distro version %q", doc.Distro.Version)
	}
	major, minor := parts[0], parts[1]
	type spec struct{ id, pkg, sev, desc string }
	var advs []grypedb.Advisory
	for _, s := range []spec{
		{"CVE-2026-31407", "libcrypto3", "high", "OpenSSL: out-of-bounds read while parsing a crafted X.509 certificate chain"},
		{"CVE-2026-31407", "libssl3", "high", "OpenSSL: out-of-bounds read while parsing a crafted X.509 certificate chain"},
		{"CVE-2026-27118", "libcurl", "medium", "libcurl: cookies are sent to the wrong host after a redirect with a crafted Location header"},
		{"CVE-2026-29873", "libxml2", "medium", "libxml2: NULL pointer dereference in the schema validator"},
		{"CVE-2026-22654", "libpng", "low", "libpng: memory leak when reading a truncated ancillary chunk"},
	} {
		v, ok := have[s.pkg]
		if !ok {
			h.t.Logf("package %s not in nginx:alpine, skipping %s", s.pkg, s.id)
			continue
		}
		fixed := bumpApk(v, 2)
		advs = append(advs, grypedb.Advisory{
			ID: s.id, Package: s.pkg, Ecosystem: "apk", Constraint: "< " + fixed,
			VersionType: "apk", FixedVersion: fixed, Severity: s.sev, Description: s.desc,
			OSName: "alpine", OSMajor: major, OSMinor: minor,
		})
	}
	if len(advs) == 0 {
		h.t.Fatal("no advisory matches a package of nginx:alpine")
	}
	h.listing.publish(h.t, h.fixtureDir, time.Now().UTC().Add(-time.Minute), advs)
	waitFor(h.t, 90*time.Second, "vulnerability database ready", func() bool {
		var out struct {
			DB map[string]any `json:"db"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &out)
		return status == 200 && out.DB["status"] == "ok"
	})
}

var apkVer = regexp.MustCompile(`^(.*?)(\d+)(-r\d+)$`)

// bumpApk raises the last numeric component before the "-rN" suffix by n:
// 3.5.9-r0 becomes 3.5.11-r0, so the installed version is below the fix.
func bumpApk(v string, n int) string {
	m := apkVer.FindStringSubmatch(v)
	if m == nil {
		return "99999.0.0-r0"
	}
	last, _ := strconv.Atoi(m[2])
	return fmt.Sprintf("%s%d%s", m[1], last+n, m[3])
}

// securityReady is true once every container has a completed scan, the
// findings include CVEs and a misconfiguration exists. Containers whose scan
// never ran or was superseded are asked to scan again (at most every 30 s).
func (h *harness) securityReady(nudged map[string]time.Time) bool {
	var vulns struct {
		Containers []struct {
			ContainerID string `json:"containerId"`
			Findings    []any  `json:"findings"`
			Scan        struct {
				Status string `json:"status"`
			} `json:"scan"`
		} `json:"containers"`
	}
	var sec struct {
		Misconfigs []any `json:"misconfigs"`
	}
	if s, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &vulns); s != 200 {
		return false
	}
	if s, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/security", nil, &sec); s != 200 {
		return false
	}
	n, allOK := 0, len(vulns.Containers) >= 4
	for _, c := range vulns.Containers {
		n += len(c.Findings)
		if c.Scan.Status != "ok" {
			allOK = false
			if time.Since(nudged[c.ContainerID]) > 30*time.Second {
				nudged[c.ContainerID] = time.Now()
				h.api(h.admin, "POST", "/api/hosts/"+h.hostID+"/containers/"+c.ContainerID+"/scan", nil)
			}
		}
	}
	return allOK && n >= 4 && len(sec.Misconfigs) > 0
}

// operatorScript is the scripted model: it looks at containers and the
// worker's logs, then answers with statements citing those evidence records.
func operatorScript(hostID string) scriptFn {
	return func(first string, msgs []oaMsg) reply {
		incident := contains(first, "Explain what happened in this incident")
		switch {
		case incident, contains(first, "worker"), contains(first, "containers"):
		default:
			return reply{Text: `{"statements":[],"actions":[]}`} // judge, briefing: nothing to say
		}
		res := toolResults(msgs)
		crash, cve := false, false
		if incident {
			data, _ := res["list_events"]["data"].(map[string]any)
			alerts, _ := data["alerts"].([]any)
			for _, a := range alerts {
				m, _ := a.(map[string]any)
				crash = crash || m["rule"] == "crash_loop"
				cve = cve || m["rule"] == "new_cve"
			}
		}
		needLogs := !incident || crash
		if needLogs && res["list_containers"] == nil {
			return reply{Calls: []call{{Name: "list_containers", Args: map[string]any{"host_id": hostID}}}}
		}
		listEv := evidenceID(msgs, "list_containers")
		if !incident && contains(first, "Which containers") {
			return reply{Text: jsonText(map[string]any{"statements": []any{
				map[string]any{"text": "shop-web, shop-api and shop-db are running on hel1-prod.", "evidence": []string{listEv}},
				map[string]any{"text": "worker is not stable: it exits right after it starts and Docker keeps restarting it.", "evidence": []string{listEv}},
			}, "actions": []any{}})}
		}
		if needLogs && res["get_logs"] == nil {
			now := time.Now().UTC()
			return reply{Calls: []call{{Name: "get_logs", Args: map[string]any{
				"container_id": containerIDByName(res["list_containers"], "worker"),
				"from":         now.Add(-time.Hour).Format(time.RFC3339),
				"to":           now.Add(time.Minute).Format(time.RFC3339),
				"max_lines":    40,
			}}}}
		}
		logEv := evidenceID(msgs, "get_logs")
		sts := []any{}
		if incident {
			evEv := evidenceID(msgs, "list_events")
			if crash {
				sts = append(sts, map[string]any{
					"text":     "The worker container exited with code 1 and was restarted by Docker repeatedly, which is what raised the crash loop alert.",
					"evidence": []string{evEv},
				})
			}
			if cve {
				sts = append(sts, map[string]any{
					"text":     "The incident also lists a new high severity vulnerability in libcrypto3 on shop-web, with a fixed package version available. It is not related to the restarts.",
					"evidence": []string{evEv},
				})
			}
		}
		if needLogs {
			sts = append(sts,
				map[string]any{
					"text":     "Before every exit the worker logs \"connect to redis:6379 failed: connection refused\" and then stops because it cannot start without a queue backend.",
					"evidence": []string{logEv},
				},
				map[string]any{
					"text":     "So the likely cause is that the Redis service the worker depends on is not reachable from this host. There is no Redis container among the running containers.",
					"evidence": []string{listEv, logEv},
				})
		}
		return reply{Text: jsonText(map[string]any{"statements": sts, "actions": []any{}})}
	}
}

// containerIDByName finds the container with the given name in a
// list_containers result (names sit under the "untrusted" key).
func containerIDByName(v any, name string) string {
	switch x := v.(type) {
	case map[string]any:
		if x["name"] == name {
			if id, _ := x["container_id"].(string); id != "" {
				return id
			}
		}
		for _, c := range x {
			if id := containerIDByName(c, name); id != "" {
				return id
			}
		}
	case []any:
		for _, c := range x {
			if id := containerIDByName(c, name); id != "" {
				return id
			}
		}
	}
	return ""
}

// ensureWebBuilt rebuilds the embedded UI when it is missing or older than web/src.
func ensureWebBuilt(t *testing.T, root string) {
	t.Helper()
	index := filepath.Join(root, "internal", "webui", "app", "index.html")
	st, err := os.Stat(index)
	stale := err != nil
	if !stale {
		_ = filepath.Walk(filepath.Join(root, "web", "src"), func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && fi.ModTime().After(st.ModTime()) {
				stale = true
			}
			return nil
		})
	}
	if !stale {
		return
	}
	cmd := exec.Command("make", "web")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make web: %v\n%s", err, out)
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

type chatMsg struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Channel   string `json:"channel"`
	Direction string `json:"direction"`
	Body      string `json:"body"`
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

// askChat posts a question as the admin and waits for the operator's answer.
func (h *harness) askChat(t *testing.T, q string) {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range h.listChat() {
		seen[m.ID] = true
	}
	var out struct {
		Reply string `json:"reply"`
	}
	if status, raw := h.api(h.admin, "POST", "/api/chat/messages", map[string]string{"body": q}, &out); status != 200 {
		t.Fatalf("post question: %d %s", status, raw)
	}
	if out.Reply != "" {
		t.Fatalf("immediate reply %q, want an operator answer", out.Reply)
	}
	waitFor(t, 90*time.Second, "answer to "+q, func() bool {
		for _, m := range h.listChat() {
			if !seen[m.ID] && m.Direction == "out" && m.Kind == "answer" {
				if m.Body == "cause not found" {
					t.Fatalf("operator answered %q to %q", m.Body, q)
				}
				return true
			}
		}
		return false
	})
}

// removeSharedTestDB removes the shared integration-test PostgreSQL container
// if it exists, so the agent does not list it as an extra container on the
// demo host. It only holds throwaway test databases and is recreated by the
// next integration test that needs it.
func removeSharedTestDB(t *testing.T) {
	t.Helper()
	const name = "haetae-test-postgres"
	if out, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "name=^"+name+"$").Output(); len(strings.TrimSpace(string(out))) > 0 {
		if err := exec.Command("docker", "rm", "-f", name).Run(); err != nil {
			t.Fatalf("docker rm %s: %v", name, err)
		}
	}
}
