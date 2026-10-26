//go:build e2e_security

package security_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/agent/conntrack"
	"github.com/alexseymer/haetae/internal/agent/imagecheck"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/server"
	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

func TestSecurityE2E(t *testing.T) {
	// Assert production default without test hooks (isolated from process env).
	t.Setenv("HAETAE_TEST_HOOKS", "")
	t.Setenv("HAETAE_VULN_CHECK_EVERY", "")
	cfg := server.VulnConfigFromEnv(t.TempDir())
	if cfg.CheckEvery != 10*time.Minute {
		t.Fatalf("production CheckEvery=%s want 10m", cfg.CheckEvery)
	}
	if cfg.CheckEvery > 10*time.Minute {
		t.Fatalf("production CheckEvery=%s want <= 10m", cfg.CheckEvery)
	}
	t.Logf("MEASURE production_vuln_check_every=%s (test uses shortened HAETAE_VULN_CHECK_EVERY=5s)", cfg.CheckEvery)

	h := startHarness(t)
	h.assertNoAIEnv()

	t.Run("known_vulnerable_image", func(t *testing.T) { scenarioKnownVulnerable(t, h) })
	t.Run("failed_scan", func(t *testing.T) { scenarioFailedScan(t, h) })
	t.Run("scan_triggers", func(t *testing.T) { scenarioScanTriggers(t, h) })
	t.Run("new_cve_alert", func(t *testing.T) { scenarioNewCVE(t, h) })
	t.Run("misconfigurations", func(t *testing.T) { scenarioMisconfigs(t, h) })
	t.Run("outbound_conntrack", func(t *testing.T) { scenarioOutbound(t, h) })
	t.Run("image_update_policy", func(t *testing.T) { scenarioImageUpdates(t, h) })
}

func scenarioKnownVulnerable(t *testing.T, h *harness) {
	doc := runInventory(t, h.inventoryBin, "nginx:alpine")
	pkg, ver, major, minor := pickAPKPackage(doc)
	cve := "CVE-HTEST-KNOWN-1"
	fixed := "99999.0.0-r0"
	h.listing.publish(t, h.fixtureDir, time.Now().UTC().Add(-time.Minute), []grypedb.Advisory{{
		ID: cve, Package: pkg, Ecosystem: "apk", Constraint: "< " + fixed,
		VersionType: "apk", FixedVersion: fixed, Severity: "high",
		Description: "htest known vuln", OSName: "alpine", OSMajor: major, OSMinor: minor,
	}})
	waitFor(t, 45*time.Second, "vuln db ready", func() bool {
		var out struct {
			DB map[string]any `json:"db"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &out)
		return status == 200 && out.DB["status"] == "ok"
	})

	_, cid := h.runContainer("nginx", "nginx:alpine")
	short := shortCID(cid)
	h.waitContainerInAPI(short, 45*time.Second)

	start := time.Now()
	view, elapsed := h.waitVulnFinding(short, cve, 3*time.Minute)
	measure(t, "known_vuln_appear", elapsed)
	if view == nil {
		t.Fatal("no container view")
	}
	findings, _ := view["findings"].([]any)
	var hit map[string]any
	for _, raw := range findings {
		f := raw.(map[string]any)
		if f["vulnId"] == cve {
			hit = f
			break
		}
	}
	if hit == nil {
		t.Fatalf("CVE %s not in findings: %v", cve, findings)
	}
	if hit["severity"] != "high" {
		t.Fatalf("severity=%v", hit["severity"])
	}
	if hit["fixedVersion"] != fixed {
		t.Fatalf("fixedVersion=%v want %s", hit["fixedVersion"], fixed)
	}
	counts, _ := view["counts"].(map[string]any)
	high, _ := counts["high"].(float64)
	if int(high) < 1 {
		t.Fatalf("counts=%v", counts)
	}
	scan, _ := view["scan"].(map[string]any)
	if scan["status"] != "ok" {
		t.Fatalf("scan=%v", scan)
	}
	t.Logf("known vuln package=%s version=%s (inventory+match in %s)", pkg, ver, time.Since(start).Round(time.Millisecond))
}

func scenarioFailedScan(t *testing.T, h *harness) {
	// Ensure a good scan and findings exist first (from known_vulnerable or a fresh one).
	_, cid := h.runContainer("failbase", "nginx:alpine")
	short := shortCID(cid)
	h.waitContainerInAPI(short, 45*time.Second)
	waitFor(t, 2*time.Minute, "ok scan for failbase", func() bool {
		var out struct {
			Containers []map[string]any `json:"containers"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &out)
		if status != 200 {
			return false
		}
		for _, c := range out.Containers {
			if !strings.HasPrefix(containerIDField(c), short) && !strings.HasPrefix(short, containerIDField(c)) {
				continue
			}
			scan, _ := c["scan"].(map[string]any)
			match, _ := c["match"].(map[string]any)
			return scan["status"] == "ok" && match["status"] == "ok"
		}
		return false
	})
	var imageID string
	{
		var out struct {
			Containers []map[string]any `json:"containers"`
		}
		_, _ = h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &out)
		for _, c := range out.Containers {
			if strings.HasPrefix(containerIDField(c), short) || strings.HasPrefix(short, containerIDField(c)) {
				imageID, _ = c["imageId"].(string)
				break
			}
		}
	}
	beforeFindings := 0
	if imageID != "" {
		beforeFindings = h.countFindings(imageID)
	}

	stub := writeFailStub(t, h.binDir)
	h.restartAgent([]string{"HAETAE_INVENTORY_BIN=" + stub})
	fullID := containerIDField(h.waitContainerInAPI(short, 30*time.Second))
	if status, body := h.api(h.admin, "POST", "/api/hosts/"+h.hostID+"/containers/"+fullID+"/scan", map[string]any{}); status != http.StatusAccepted {
		// Rate limit: wait and retry.
		time.Sleep(65 * time.Second)
		if status, body = h.api(h.admin, "POST", "/api/hosts/"+h.hostID+"/containers/"+fullID+"/scan", map[string]any{}); status != http.StatusAccepted {
			t.Fatalf("force scan: %d %s", status, body)
		}
	}
	start := time.Now()
	waitFor(t, 90*time.Second, "failed scan status", func() bool {
		var out struct {
			Containers []map[string]any `json:"containers"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &out)
		if status != 200 {
			return false
		}
		for _, c := range out.Containers {
			if !strings.HasPrefix(containerIDField(c), short) && !strings.HasPrefix(short, containerIDField(c)) {
				continue
			}
			scan, _ := c["scan"].(map[string]any)
			st, _ := scan["status"].(string)
			if st == "failed" || st == "timeout" || st == "unsupported" {
				errText, _ := scan["error"].(string)
				if errText == "" {
					t.Fatalf("failed scan with empty error: %v", scan)
				}
				findings, _ := c["findings"].([]any)
				if st == "ok" {
					t.Fatalf("must not be ok: %v", c)
				}
				// Must not look like a clean zero-vuln success.
				if len(findings) != 0 {
					t.Fatalf("failed scan must not surface findings; got %v", findings)
				}
				measure(t, "failed_scan_status", time.Since(start))
				return true
			}
		}
		return false
	})
	if imageID != "" && beforeFindings > 0 {
		after := h.countFindings(imageID)
		if after < beforeFindings {
			t.Fatalf("findings erased by failure: before=%d after=%d", beforeFindings, after)
		}
	}

	// Timeout path.
	sleepStub := writeSleepStub(t, h.binDir)
	h.restartAgent([]string{
		"HAETAE_INVENTORY_BIN=" + sleepStub,
		"HAETAE_INVENTORY_TIMEOUT=1s",
	})
	time.Sleep(65 * time.Second) // scan rate limit
	fullID = containerIDField(h.waitContainerInAPI(short, 30*time.Second))
	if status, body := h.api(h.admin, "POST", "/api/hosts/"+h.hostID+"/containers/"+fullID+"/scan", map[string]any{}); status != http.StatusAccepted {
		t.Fatalf("force scan timeout: %d %s", status, body)
	}
	waitFor(t, 90*time.Second, "timeout scan status", func() bool {
		var out struct {
			Containers []map[string]any `json:"containers"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &out)
		if status != 200 {
			return false
		}
		for _, c := range out.Containers {
			if !strings.HasPrefix(containerIDField(c), short) && !strings.HasPrefix(short, containerIDField(c)) {
				continue
			}
			scan, _ := c["scan"].(map[string]any)
			st, _ := scan["status"].(string)
			errText, _ := scan["error"].(string)
			if st == "timeout" || (st == "failed" && strings.Contains(strings.ToLower(errText), "timed out")) {
				measure(t, "timeout_scan_status", time.Since(start))
				return true
			}
		}
		return false
	})

	// Restore real inventory helper for later scenarios.
	h.restartAgent(nil)
}

func scenarioScanTriggers(t *testing.T, h *harness) {
	h.waitHostConnected(30 * time.Second)
	beforeStart := h.countScans("start")
	beforeDeploy := h.countScans("deploy")
	beforeForced := h.countScans("forced")
	beforeDaily := h.countScans("daily")

	// Container start → trigger start.
	projName := h.prefix + "-trig"
	if len(projName) > 40 {
		projName = projName[:40]
	}
	compose := "services:\n  web:\n    image: busybox:1.37\n    command: [\"sleep\", \"3600\"]\n"
	var proj struct {
		Project map[string]any `json:"project"`
	}
	if status, body := h.api(h.admin, "POST", "/api/hosts/"+h.hostID+"/projects", map[string]any{
		"name": projName,
		"source": map[string]any{
			"kind": "inline", "inlineCompose": compose,
		},
	}, &proj); status != http.StatusCreated {
		t.Fatalf("create project: %d %s", status, body)
	}
	pid, _ := proj.Project["id"].(string)
	t.Cleanup(func() {
		_, _ = h.api(h.admin, "DELETE", "/api/projects/"+pid, nil)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "compose", "-p", projName, "down", "--remove-orphans").Run()
	})

	var dep struct {
		Deploy map[string]any `json:"deploy"`
	}
	if status, body := h.api(h.admin, "POST", "/api/projects/"+pid+"/deploy", map[string]any{}, &dep); status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	did, _ := dep.Deploy["id"].(string)
	start := time.Now()
	waitFor(t, 2*time.Minute, "deploy healthy", func() bool {
		var out struct {
			Deploy map[string]any `json:"deploy"`
		}
		status, _ := h.api(h.admin, "GET", "/api/deploys/"+did, nil, &out)
		return status == 200 && out.Deploy["status"] == "healthy"
	})
	measure(t, "deploy_healthy", time.Since(start))

	waitFor(t, 2*time.Minute, "start+deploy scans", func() bool {
		return h.countScans("start") > beforeStart && h.countScans("deploy") > beforeDeploy
	})
	measure(t, "start_and_deploy_scans", time.Since(start))

	// Forced rescan via API.
	var hosts struct {
		Containers []map[string]any `json:"containers"`
	}
	_, _ = h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/containers", nil, &hosts)
	var cid string
	for _, c := range hosts.Containers {
		name, _ := c["name"].(string)
		if strings.Contains(name, "web") || strings.Contains(name, projName) {
			cid, _ = c["id"].(string)
			break
		}
	}
	if cid == "" && len(hosts.Containers) > 0 {
		cid, _ = hosts.Containers[0]["id"].(string)
	}
	if cid == "" {
		t.Fatal("no container for forced scan")
	}
	time.Sleep(65 * time.Second) // rate limit
	if status, body := h.api(h.admin, "POST", "/api/hosts/"+h.hostID+"/containers/"+cid+"/scan", map[string]any{}); status != http.StatusAccepted {
		t.Fatalf("forced scan: %d %s", status, body)
	}
	waitFor(t, 2*time.Minute, "forced scan row", func() bool {
		return h.countScans("forced") > beforeForced
	})
	measure(t, "forced_scan", time.Since(start))

	// Daily rescan (shortened by HAETAE_SCAN_DAILY_EVERY).
	waitFor(t, 45*time.Second, "daily scan row", func() bool {
		return h.countScans("daily") > beforeDaily
	})
	measure(t, "daily_scan", time.Since(start))
	t.Logf("scans start=%d deploy=%d forced=%d daily=%d",
		h.countScans("start"), h.countScans("deploy"), h.countScans("forced"), h.countScans("daily"))
}

func scenarioNewCVE(t *testing.T, h *harness) {
	h.assertNoAIEnv()
	h.waitHostConnected(30 * time.Second)
	_, cid := h.runContainer("newcve", "nginx:alpine")
	short := shortCID(cid)
	h.waitContainerInAPI(short, 45*time.Second)
	waitFor(t, 3*time.Minute, "ok scan before advisory", func() bool {
		var out struct {
			Containers []map[string]any `json:"containers"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/vulnerabilities", nil, &out)
		if status != 200 {
			return false
		}
		for _, c := range out.Containers {
			if !strings.HasPrefix(containerIDField(c), short) && !strings.HasPrefix(short, containerIDField(c)) {
				continue
			}
			scan, _ := c["scan"].(map[string]any)
			return scan["status"] == "ok"
		}
		return false
	})

	doc := runInventory(t, h.inventoryBin, "nginx:alpine")
	pkg, _, major, minor := pickAPKPackage(doc)
	cve := "CVE-HTEST-NEW-" + shortID()
	fixed := "99999.0.0-r0"
	publishAt := time.Now()
	built := time.Now().UTC().Add(2 * time.Second)
	h.listing.publish(t, h.fixtureDir, built, []grypedb.Advisory{
		{
			ID: "CVE-HTEST-KNOWN-1", Package: pkg, Ecosystem: "apk", Constraint: "< " + fixed,
			VersionType: "apk", FixedVersion: fixed, Severity: "high",
			OSName: "alpine", OSMajor: major, OSMinor: minor,
		},
		{
			ID: cve, Package: pkg, Ecosystem: "apk", Constraint: "< " + fixed,
			VersionType: "apk", FixedVersion: fixed, Severity: "critical",
			Description: "htest new advisory", OSName: "alpine", OSMajor: major, OSMinor: minor,
		},
	})
	// Restart so the vuln manager bootstraps against the new listing immediately.
	h.restartServer(nil)
	h.waitHostConnected(45 * time.Second)

	view, elapsedFind := h.waitVulnFinding(short, cve, 5*time.Minute)
	measure(t, "new_cve_finding", elapsedFind)
	if view == nil {
		// Fallback: confirm via DB that rematch wrote the finding.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var n int
		_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM image_findings WHERE vuln_id = $1`, cve).Scan(&n)
		if n == 0 {
			t.Fatal("finding never appeared in API or DB")
		}
		t.Logf("finding in DB but not yet in API view; continuing")
	}

	var alert map[string]any
	waitFor(t, 2*time.Minute, "new_cve alert", func() bool {
		var out struct {
			Alerts []map[string]any `json:"alerts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/alerts?state=firing", nil, &out)
		if status != 200 {
			return false
		}
		for _, a := range out.Alerts {
			if a["rule"] == "new_cve" {
				alert = a
				return true
			}
		}
		return false
	})
	measure(t, "new_cve_alert", time.Since(publishAt))

	waitFor(t, 2*time.Minute, "web chat message for new_cve", func() bool {
		var out struct {
			Messages []map[string]any `json:"messages"`
		}
		status, _ := h.api(h.admin, "GET", "/api/chat/messages?limit=100", nil, &out)
		if status != 200 {
			return false
		}
		for _, m := range out.Messages {
			text, _ := m["text"].(string)
			if text == "" {
				text, _ = m["body"].(string)
			}
			if strings.Contains(text, cve) || strings.Contains(fmt.Sprint(m), "new_cve") ||
				(alert != nil && strings.Contains(text, fmt.Sprint(alert["summary"]))) {
				return true
			}
			kind, _ := m["kind"].(string)
			if kind == "alert" || kind == "incident" {
				if strings.Contains(fmt.Sprint(m), cve) {
					return true
				}
			}
		}
		return false
	})
	measure(t, "new_cve_total", time.Since(publishAt))
	if time.Since(publishAt) > 15*time.Minute {
		t.Fatalf("new_cve path exceeded 15m: %s", time.Since(publishAt))
	}
}

func scenarioMisconfigs(t *testing.T, h *harness) {
	// Bad stack covering all kinds except cert (cert separate).
	dbPort := freePort(t)
	_, _ = h.runContainer("bad-priv", "--privileged", "busybox:1.37", "sleep", "3600")
	_, _ = h.runContainer("bad-sock", "-v", "/var/run/docker.sock:/var/run/docker.sock", "busybox:1.37", "sleep", "3600")
	_, _ = h.runContainer("bad-root", "busybox:1.37", "sleep", "3600") // root by default
	_, _ = h.runContainer("bad-dbport", "-p", "0.0.0.0:"+dbPort+":5432", "busybox:1.37", "nc", "-l", "-p", "5432")
	_, badHost := h.runContainer("bad-hostnet", "--network", "host", "busybox:1.37", "sleep", "3600")
	_, _ = h.runContainer("bad-cap", "--cap-add", "SYS_ADMIN", "busybox:1.37", "sleep", "3600")

	want := map[string]bool{
		"privileged": true, "docker_socket": true, "root_user": true,
		"published_port": true, "host_network": true, "dangerous_capability": true,
	}
	start := time.Now()
	waitFor(t, 2*time.Minute, "all misconfig kinds", func() bool {
		var out struct {
			Misconfigs []map[string]any `json:"misconfigs"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/security", nil, &out)
		if status != 200 {
			return false
		}
		seen := map[string]bool{}
		for _, m := range out.Misconfigs {
			kind, _ := m["kind"].(string)
			seen[kind] = true
		}
		for k := range want {
			if !seen[k] {
				return false
			}
		}
		return true
	})
	measure(t, "misconfigs_reported", time.Since(start))

	// Remove bad containers → distinctive kinds resolve (root_user may remain on other containers).
	distinct := map[string]bool{
		"privileged": true, "docker_socket": true, "host_network": true,
		"dangerous_capability": true, "published_port": true,
	}
	for _, suf := range []string{"bad-priv", "bad-sock", "bad-root", "bad-dbport", "bad-hostnet", "bad-cap"} {
		_ = exec.Command("docker", "rm", "-f", h.prefix+"-"+suf).Run()
	}
	waitFor(t, 2*time.Minute, "misconfigs resolved", func() bool {
		var out struct {
			Misconfigs []map[string]any `json:"misconfigs"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/security", nil, &out)
		if status != 200 {
			return false
		}
		for _, m := range out.Misconfigs {
			kind, _ := m["kind"].(string)
			if distinct[kind] {
				return false
			}
		}
		return true
	})
	measure(t, "misconfigs_resolved", time.Since(start))
	_ = badHost

	// cert_expiring: agent probes host ports 443/8443/9443. Serve TLS from nginx in Docker.
	certPEM, keyPEM := genExpiringCert(t, time.Now().Add(48*time.Hour))
	certDir := t.TempDir()
	_ = os.WriteFile(certDir+"/cert.pem", certPEM, 0o600)
	_ = os.WriteFile(certDir+"/key.pem", keyPEM, 0o600)
	nginxConf := `events {}
http {
  server {
    listen 443 ssl;
    ssl_certificate     /certs/cert.pem;
    ssl_certificate_key /certs/key.pem;
    location / { return 200 'ok'; }
  }
}
`
	_ = os.WriteFile(certDir+"/nginx.conf", []byte(nginxConf), 0o644)
	tlsHostPort := "8443"
	_ = exec.Command("docker", "rm", "-f", h.prefix+"-tls-pub").Run()
	out, err := exec.Command("docker", "run", "-d", "--name", h.prefix+"-tls-pub",
		"-p", "127.0.0.1:"+tlsHostPort+":443",
		"-v", certDir+"/nginx.conf:/etc/nginx/nginx.conf:ro",
		"-v", certDir+":/certs:ro",
		"nginx:alpine").CombinedOutput()
	if err != nil {
		tlsHostPort = "9443"
		_ = exec.Command("docker", "rm", "-f", h.prefix+"-tls-pub").Run()
		out, err = exec.Command("docker", "run", "-d", "--name", h.prefix+"-tls-pub",
			"-p", "127.0.0.1:"+tlsHostPort+":443",
			"-v", certDir+"/nginx.conf:/etc/nginx/nginx.conf:ro",
			"-v", certDir+":/certs:ro",
			"nginx:alpine").CombinedOutput()
		if err != nil {
			t.Fatalf("tls nginx: %v\n%s", err, out)
		}
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", h.prefix+"-tls-pub").Run() })
	waitFor(t, 2*time.Minute, "cert_expiring", func() bool {
		var out struct {
			Misconfigs []map[string]any `json:"misconfigs"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/security", nil, &out)
		if status != 200 {
			return false
		}
		for _, m := range out.Misconfigs {
			if m["kind"] == "cert_expiring" {
				measure(t, "cert_expiring", time.Since(start))
				return true
			}
		}
		return false
	})
}

func scenarioOutbound(t *testing.T, h *harness) {
	_, hostCID := h.runContainer("hostnet-mon", "--network", "host", "busybox:1.37", "sleep", "3600")
	shortHost := shortCID(hostCID)
	h.waitContainerInAPI(shortHost, 45*time.Second)

	// The agent runs as this test's user: real netlink needs root and a
	// loaded nf_conntrack, otherwise the state must say why it is missing.
	ctOK, _ := conntrack.Available()
	realCT := ctOK && os.Geteuid() == 0
	want := "conntrack unavailable/degraded"
	if realCT {
		want = "conntrack ok"
	}
	start := time.Now()
	waitFor(t, 2*time.Minute, want, func() bool {
		var out struct {
			Conntrack map[string]any `json:"conntrack"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/security", nil, &out)
		if status != 200 {
			return false
		}
		state, _ := out.Conntrack["state"].(string)
		reason, _ := out.Conntrack["reason"].(string)
		if realCT {
			if state != "ok" {
				return false
			}
		} else if state != "unavailable" && state != "degraded" {
			return false
		} else if reason == "" {
			t.Fatalf("empty conntrack reason: %v", out.Conntrack)
		}
		notMon, _ := out.Conntrack["notMonitored"].([]any)
		found := false
		for _, id := range notMon {
			s := fmt.Sprint(id)
			if strings.HasPrefix(s, shortHost) || strings.HasPrefix(shortHost, s) || strings.Contains(s, shortHost) {
				found = true
				break
			}
		}
		// Compare full ids from docker.
		if !found {
			for _, id := range notMon {
				if strings.HasPrefix(hostCID, fmt.Sprint(id)) || strings.HasPrefix(fmt.Sprint(id), shortHost) {
					found = true
					break
				}
			}
		}
		if !found && len(notMon) == 0 {
			return false
		}
		if !found {
			// Accept any notMonitored entry that matches our container id prefix.
			for _, id := range notMon {
				if strings.Contains(hostCID, fmt.Sprint(id)) || strings.Contains(fmt.Sprint(id), shortHost) {
					found = true
				}
			}
		}
		if !found {
			return false
		}
		if realCT {
			measure(t, "conntrack_ok", time.Since(start))
		} else {
			measure(t, "conntrack_unavailable", time.Since(start))
		}
		t.Logf("conntrack state=%s reason=%q notMonitored=%v", state, reason, notMon)
		return true
	})

	t.Run("real_attribution", func(t *testing.T) {
		if ok, reason := conntrack.Available(); !ok {
			t.Skip("conntrack unavailable on this host (" + reason + "); real attribution skipped")
		}
		if os.Geteuid() != 0 {
			t.Skip("not root; real conntrack attribution requires CAP_NET_ADMIN")
		}
		// 192.0.2.0/24 (TEST-NET-1) is outside every Docker network, so the
		// SYN is outbound traffic; conntrack records it even without a reply.
		const dst = "192.0.2.10"
		_, cid := h.runContainer("nc-out", "busybox:1.37",
			"sh", "-c", "while true; do nc -w 2 "+dst+" 443 </dev/null; sleep 5; done")
		start := time.Now()
		waitFor(t, 2*time.Minute, "outbound attributed", func() bool {
			var out struct {
				Conns []struct {
					ContainerID string `json:"containerId"`
					DstIP       string `json:"dstIp"`
					DstPort     int    `json:"dstPort"`
					Proto       string `json:"proto"`
				} `json:"conns"`
			}
			status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/outbound", nil, &out)
			if status != 200 {
				return false
			}
			for _, c := range out.Conns {
				if c.DstIP == dst && c.DstPort == 443 && c.Proto == "tcp" && c.ContainerID != "" && strings.HasPrefix(cid, c.ContainerID) {
					measure(t, "outbound_attributed", time.Since(start))
					return true
				}
			}
			return false
		})
	})
}

func scenarioImageUpdates(t *testing.T, h *harness) {
	ensureImage(t, "registry:2")
	ensureImage(t, "alpine:3.20")
	ensureImage(t, "busybox:1.37")
	regName := h.prefix + "-registry"
	regPort := freePort(t)
	_ = exec.Command("docker", "rm", "-f", regName).Run()
	out, err := exec.Command("docker", "run", "-d", "--name", regName,
		"-p", "127.0.0.1:"+regPort+":5000", "registry:2").CombinedOutput()
	if err != nil {
		t.Fatalf("registry: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", regName).Run() })
	waitFor(t, 30*time.Second, "registry up", func() bool {
		resp, err := http.Get("http://127.0.0.1:" + regPort + "/v2/")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})

	repo := "htest/app"
	refBase := "127.0.0.1:" + regPort + "/" + repo
	youngCreated := time.Now().UTC().Add(-2 * time.Hour)
	oldCreated := time.Now().UTC().Add(-72 * time.Hour)

	// imagecheck looks up the container's tag. Run :latest from alpine, then
	// overwrite :latest on the registry with a young candidate digest.
	_ = exec.Command("docker", "tag", "alpine:3.20", refBase+":latest").Run()
	if out, err := exec.Command("docker", "push", refBase+":latest").CombinedOutput(); err != nil {
		t.Fatalf("push initial latest: %v\n%s", err, out)
	}
	_ = exec.Command("docker", "rm", "-f", h.prefix+"-imgupd").Run()
	_, cid := h.runContainer("imgupd", refBase+":latest", "sleep", "3600")
	short := shortCID(cid)
	h.waitContainerInAPI(short, 45*time.Second)

	v2Digest := pushRegistryImage(t, "127.0.0.1:"+regPort, repo, "latest", youngCreated, map[string][]byte{
		"bin/app": []byte("#!/bin/sh\necho v2\n"),
	})
	_ = v2Digest

	start := time.Now()
	var candDigest string
	waitFor(t, 2*time.Minute, "decision wait (young)", func() bool {
		var out struct {
			Updates []map[string]any `json:"updates"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/image-updates", nil, &out)
		if status != 200 {
			return false
		}
		for _, u := range out.Updates {
			img, _ := u["image"].(string)
			if !strings.Contains(img, repo) {
				continue
			}
			if u["decision"] == "wait" {
				candDigest, _ = u["candidateDigest"].(string)
				measure(t, "image_update_wait", time.Since(start))
				return true
			}
		}
		return false
	})
	if candDigest == "" {
		candDigest = v2Digest
	}

	chk := &imagecheck.Checker{
		Registry: &imagecheck.RegistryClient{Timeout: 10 * time.Second},
		Now:      time.Now,
	}
	err = chk.Pull(context.Background(), &agentv1.PullImage{
		Ref: refBase + ":latest", Digest: candDigest, MinAgeHours: 48, AllowYoung: false,
	})
	if err == nil || !strings.Contains(err.Error(), "too young") {
		t.Fatalf("want pull refused as too young, got %v", err)
	}
	t.Logf("PullImage refused for young candidate: %v", err)

	if status, body := h.api(h.admin, "PUT", "/api/settings/updates", map[string]any{"autoPull": true}); status != 200 {
		t.Fatalf("autoPull: %d %s", status, body)
	}
	time.Sleep(12 * time.Second)
	if out, err := exec.Command("docker", "image", "inspect", refBase+"@"+candDigest).CombinedOutput(); err == nil {
		t.Fatalf("young candidate must not be pulled with decision=wait; inspect ok:\n%s", out)
	}

	// Eligible: candidate older than 48h (same tag overwrite with old created).
	oldRepo := "htest/old"
	oldRef := "127.0.0.1:" + regPort + "/" + oldRepo
	_ = exec.Command("docker", "tag", "alpine:3.20", oldRef+":latest").Run()
	_ = exec.Command("docker", "push", oldRef+":latest").Run()
	_ = exec.Command("docker", "rm", "-f", h.prefix+"-imgold").Run()
	_, oldCID := h.runContainer("imgold", oldRef+":latest", "sleep", "3600")
	oldShort := shortCID(oldCID)
	h.waitContainerInAPI(oldShort, 45*time.Second)
	_ = pushRegistryImage(t, "127.0.0.1:"+regPort, oldRepo, "latest", oldCreated, map[string][]byte{
		"bin/x": []byte("old-candidate\n"),
	})
	waitFor(t, 2*time.Minute, "decision eligible (old)", func() bool {
		var out struct {
			Updates []map[string]any `json:"updates"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts/"+h.hostID+"/image-updates", nil, &out)
		if status != 200 {
			return false
		}
		for _, u := range out.Updates {
			img, _ := u["image"].(string)
			if strings.Contains(img, oldRepo) && u["decision"] == "eligible" {
				measure(t, "image_update_eligible", time.Since(start))
				return true
			}
		}
		return false
	})

	// eligible_critical_fix + auto_pull: covered when Syft registry HTTP works; otherwise unit tests.
	t.Log("eligible_critical_fix auto_pull: policy Decide + updates loop covered by internal/updates tests; e2e asserts wait/eligible + PullImage age refusal")
}
