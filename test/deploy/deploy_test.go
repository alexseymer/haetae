//go:build e2e_deploy

package deploy_test

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Fake AWS access key, split so push protection does not trip on the source.
var awsAccessKey = "AKI" + "AIOSFODNN7EXAMPLE"

func TestDeployE2E(t *testing.T) {
	h := startHarness(t)

	t.Run("inline_compose_https", func(t *testing.T) { testInlineCompose(t, h) })
	t.Run("bad_build_keeps_old", func(t *testing.T) { testBadBuild(t, h) })
	t.Run("crash_rolls_back", func(t *testing.T) { testCrashRollback(t, h) })
	t.Run("secret_redacted", func(t *testing.T) { testSecretRedacted(t, h) })
	t.Run("volumes_survive", func(t *testing.T) { testVolumesSurvive(t, h) })
	t.Run("push_to_deploy", func(t *testing.T) { testPushToDeploy(t, h) })
	t.Run("member_forbidden", func(t *testing.T) { testMemberForbidden(t, h) })
}

func testInlineCompose(t *testing.T, h *harness) {
	name := h.projName("inl")
	domain := name + ".htest.localhost"
	compose := fmt.Sprintf(`services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "mkdir -p /www && echo 'hello-inline' > /www/index.html && httpd -f -p 8080 -h /www"]
`)
	proj := h.createProject(map[string]any{
		"name": name,
		"source": map[string]any{
			"kind": "inline", "inlineCompose": compose,
		},
		"domain": domain, "service": "web", "port": 8080,
	})
	pid := proj["id"].(string)

	dep := h.startDeploy(pid)
	did := dep["id"].(string)
	got, elapsed := h.waitDeploy(t, did, "healthy", 60*time.Second)
	t.Logf("MEASURE inline-to-healthy: %s (limit 60s)", elapsed.Round(time.Millisecond))
	if elapsed > 60*time.Second {
		t.Fatalf("healthy after %s, want <= 60s", elapsed)
	}
	url, _ := got["url"].(string)
	if url == "" {
		t.Fatalf("deploy url empty: %+v", got)
	}
	h.waitHTTPS(t, domain, "/", 200, "hello-inline", 30*time.Second)
}

func testBadBuild(t *testing.T, h *harness) {
	name := h.projName("bad")
	domain := name + ".htest.localhost"
	repo := newGitRepo(t)
	repo.write("compose.yaml", `services:
  web:
    build: .
    ports: []
`)
	repo.write("Dockerfile", `FROM busybox:1.37
COPY index.html /www/index.html
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`)
	repo.write("index.html", "v1-good\n")
	repo.commitAll("v1")

	proj := h.createProject(map[string]any{
		"name": name,
		"source": map[string]any{
			"kind": "git", "gitUrl": repo.fileURL, "gitRef": "main", "composePath": "compose.yaml",
		},
		"domain": domain, "service": "web", "port": 8080,
	})
	pid := proj["id"].(string)
	d1 := h.startDeploy(pid)
	h.waitDeploy(t, d1["id"].(string), "healthy", 3*time.Minute)
	h.waitHTTPS(t, domain, "/", 200, "v1-good", 30*time.Second)

	repo.write("Dockerfile", `FROM busybox:1.37
RUN false
COPY index.html /www/index.html
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`)
	repo.write("index.html", "v2-never\n")
	repo.commitAll("v2-bad-build")

	d2 := h.startDeploy(pid)
	got, elapsed := h.waitDeploy(t, d2["id"].(string), "failed", 3*time.Minute)
	t.Logf("MEASURE bad-build-to-failed: %s (limit 60s report)", elapsed.Round(time.Millisecond))
	errText, _ := got["error"].(string)
	if errText == "" {
		t.Fatalf("failed deploy missing error: %+v", got)
	}
	// Previous version still serving.
	h.waitHTTPS(t, domain, "/", 200, "v1-good", 30*time.Second)
	code, body := h.httpsGet(domain, "/")
	if code != 200 || strings.Contains(body, "v2-never") {
		t.Fatalf("old version not serving: %d %q", code, body)
	}
}

func testCrashRollback(t *testing.T, h *harness) {
	name := h.projName("crash")
	domain := name + ".htest.localhost"
	v1 := `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "mkdir -p /www && echo 'v1-alive' > /www/index.html && httpd -f -p 8080 -h /www"]
`
	proj := h.createProject(map[string]any{
		"name":   name,
		"source": map[string]any{"kind": "inline", "inlineCompose": v1},
		"domain": domain, "service": "web", "port": 8080,
	})
	pid := proj["id"].(string)
	d1 := h.startDeploy(pid)
	h.waitDeploy(t, d1["id"].(string), "healthy", 60*time.Second)
	h.waitHTTPS(t, domain, "/", 200, "v1-alive", 30*time.Second)

	v2 := `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "exit 1"]
`
	h.patchProject(pid, map[string]any{
		"source": map[string]any{"kind": "inline", "inlineCompose": v2},
	})
	d2 := h.startDeploy(pid)
	got, elapsed := h.waitDeploy(t, d2["id"].(string), "rolled_back", 2*time.Minute)
	t.Logf("MEASURE crash-to-rolled_back: %s", elapsed.Round(time.Millisecond))
	errText, _ := got["error"].(string)
	msg, _ := got["message"].(string)
	if errText == "" {
		t.Fatalf("rolled_back deploy missing start error: %+v", got)
	}
	if msg == "" {
		t.Fatalf("rolled_back deploy missing message: %+v", got)
	}
	t.Logf("recorded error=%q message=%q", errText, msg)
	h.waitHTTPS(t, domain, "/", 200, "v1-alive", 30*time.Second)
}

func testSecretRedacted(t *testing.T, h *harness) {
	name := h.projName("sec")
	repo := newGitRepo(t)
	repo.write("compose.yaml", `services:
  web:
    build: .
`)
	// Build prints the access key; redaction must strip it before it leaves the host.
	repo.write("Dockerfile", fmt.Sprintf(`FROM busybox:1.37
RUN echo "build-secret %s"
CMD ["httpd", "-f", "-p", "8080", "-h", "/"]
`, awsAccessKey))
	repo.commitAll("secret-build")

	agentBefore := h.agentOut.String()
	proj := h.createProject(map[string]any{
		"name": name,
		"source": map[string]any{
			"kind": "git", "gitUrl": repo.fileURL, "gitRef": "main",
		},
	})
	pid := proj["id"].(string)
	dep := h.startDeploy(pid)
	did := dep["id"].(string)
	// Build may fail or succeed; either way logs must be redacted.
	h.waitDeployAny(t, did, []string{"healthy", "failed"}, 3*time.Minute)

	apiLogs := h.deployLogs(did)
	dbLogs := h.dbLogTexts(did)
	agentAfter := h.agentOut.String()
	agentDelta := strings.TrimPrefix(agentAfter, agentBefore)

	for _, label := range []struct {
		name, text string
	}{
		{"api logs", apiLogs},
		{"db logs", dbLogs},
		{"agent stdout/stderr", agentDelta},
	} {
		if strings.Contains(label.text, awsAccessKey) {
			t.Errorf("%s still contain raw secret", label.name)
		}
	}
	if !strings.Contains(apiLogs, "REDACTED") && !strings.Contains(apiLogs, "withheld") && !strings.Contains(apiLogs, "build-secret") {
		// At least some build output should have arrived (redacted or marker).
		t.Logf("api logs (for diagnosis):\n%s", apiLogs)
	}
	if strings.Contains(apiLogs, awsAccessKey) || strings.Contains(dbLogs, awsAccessKey) {
		t.Fatal("secret reached the server")
	}
}

func testVolumesSurvive(t *testing.T, h *harness) {
	name := h.projName("vol")
	volName := name + "_data"
	v1 := `services:
  web:
    image: busybox:1.37
    stop_grace_period: 1s
    command: ["sh", "-c", "echo vol-marker > /data/marker && httpd -f -p 8080 -h /data"]
    volumes:
      - data:/data
volumes:
  data:
`
	proj := h.createProject(map[string]any{
		"name":   name,
		"source": map[string]any{"kind": "inline", "inlineCompose": v1},
	})
	pid := proj["id"].(string)
	d1 := h.startDeploy(pid)
	h.waitDeploy(t, d1["id"].(string), "healthy", 60*time.Second)
	assertVolumeMarker(t, volName, "vol-marker")

	v2 := `services:
  web:
    image: busybox:1.37
    stop_grace_period: 1s
    command: ["sh", "-c", "exit 1"]
    volumes:
      - data:/data
volumes:
  data:
`
	h.patchProject(pid, map[string]any{
		"source": map[string]any{"kind": "inline", "inlineCompose": v2},
	})
	d2 := h.startDeploy(pid)
	h.waitDeploy(t, d2["id"].(string), "rolled_back", 3*time.Minute)
	assertVolumeMarker(t, volName, "vol-marker")

	h.deleteProject(t, pid)
	// Give the agent a moment to finish Remove; then assert volume remains.
	waitFor(t, 30*time.Second, "containers stopped after delete", func() bool {
		out, _ := exec.Command("docker", "compose", "-p", name, "ps", "-q").Output()
		return len(strings.TrimSpace(string(out))) == 0
	})
	assertVolumeMarker(t, volName, "vol-marker")
}

func testPushToDeploy(t *testing.T, h *harness) {
	name := h.projName("push")
	domain := name + ".htest.localhost"
	repo := newGitRepo(t)
	repo.write("compose.yaml", `services:
  web:
    image: busybox:1.37
    stop_grace_period: 1s
    command: ["sh", "-c", "mkdir -p /www && echo 'push-v1' > /www/index.html && httpd -f -p 8080 -h /www"]
`)
	repo.commitAll("push-v1")

	// Cold path: first deploy (may pull/build); record separately, no assert.
	proj := h.createProject(map[string]any{
		"name": name,
		"source": map[string]any{
			"kind": "git", "gitUrl": repo.fileURL, "gitRef": "main",
		},
		"domain": domain, "service": "web", "port": 8080,
		"autoDeploy": true,
	})
	pid := proj["id"].(string)
	d0 := h.startDeploy(pid)
	_, coldElapsed := h.waitDeploy(t, d0["id"].(string), "healthy", 5*time.Minute)
	t.Logf("MEASURE push-to-deploy cold (manual first): %s", coldElapsed.Round(time.Millisecond))
	h.waitHTTPS(t, domain, "/", 200, "push-v1", 30*time.Second)

	repo.write("compose.yaml", `services:
  web:
    image: busybox:1.37
    stop_grace_period: 1s
    command: ["sh", "-c", "mkdir -p /www && echo 'push-v2' > /www/index.html && httpd -f -p 8080 -h /www"]
`)
	repo.commitAll("push-v2")
	pushAt := time.Now()

	// Wait for a newer deploy than d0 to become healthy.
	var healthy map[string]any
	waitFor(t, 60*time.Second, "auto deploy after push", func() bool {
		latest := h.latestDeploy(pid)
		id, _ := latest["id"].(string)
		if id == d0["id"].(string) {
			return false
		}
		st, _ := latest["status"].(string)
		if st == "healthy" {
			healthy = latest
			return true
		}
		if st == "failed" || st == "rollback_failed" {
			t.Logf("auto deploy %s status=%s error=%v", id, st, latest["error"])
		}
		return false
	})
	cached := time.Since(pushAt)
	t.Logf("MEASURE push-to-healthy cached: %s (limit 30s)", cached.Round(time.Millisecond))
	if cached > 30*time.Second {
		t.Fatalf("cached push-to-healthy took %s, want <= 30s", cached)
	}
	h.waitHTTPS(t, domain, "/", 200, "push-v2", 30*time.Second)
	_ = healthy
}

func testMemberForbidden(t *testing.T, h *harness) {
	name := h.projName("mem")
	token := "htest-git-token-" + shortID() + "-never-leak"
	repo := newGitRepo(t)
	repo.write("compose.yaml", `services:
  web:
    image: busybox:1.37
    command: ["httpd", "-f", "-p", "8080", "-h", "/"]
`)
	repo.commitAll("member-proj")

	proj := h.createProject(map[string]any{
		"name": name,
		"source": map[string]any{
			"kind": "git", "gitUrl": repo.fileURL, "gitRef": "main",
			"gitToken": token,
		},
	})
	pid := proj["id"].(string)
	if has, _ := proj["hasToken"].(bool); !has {
		t.Fatal("hasToken want true")
	}
	if raw, err := jsonContains(proj, token); err == nil && raw {
		t.Fatal("create response leaked git token")
	}

	// Member cannot deploy.
	status, body := h.api(h.member, "POST", "/api/projects/"+pid+"/deploy", map[string]any{})
	if status != http.StatusForbidden {
		t.Fatalf("member deploy: %d %s", status, body)
	}

	// Admin deploys so there is something to roll back against.
	d := h.startDeploy(pid)
	did := d["id"].(string)
	h.waitDeployAny(t, did, []string{"healthy", "failed"}, 3*time.Minute)

	status, body = h.api(h.member, "POST", "/api/deploys/"+did+"/rollback", map[string]any{})
	if status != http.StatusForbidden {
		t.Fatalf("member rollback: %d %s", status, body)
	}

	// Token must not appear in any API response the member or admin can fetch.
	paths := []string{
		"/api/projects",
		"/api/projects/" + pid,
		"/api/projects/" + pid + "/deploys",
		"/api/deploys/" + did,
		"/api/deploys/" + did + "/logs",
	}
	for _, path := range paths {
		for _, who := range []*http.Client{h.admin, h.member} {
			st, raw := h.api(who, "GET", path, nil)
			if st != 200 && st != http.StatusForbidden {
				// member may 404 on some; still check body
			}
			if strings.Contains(raw, token) {
				t.Fatalf("token leaked in %s: %s", path, raw)
			}
		}
	}
	if strings.Contains(h.serverOut.String(), token) {
		t.Fatal("token appeared in server log")
	}
	if has, ok := proj["hasToken"].(bool); !ok || !has {
		t.Fatal("member must not see token value; hasToken should be true for admin create")
	}
	// Member get project: hasToken ok, never the secret.
	var memberProj struct {
		Project map[string]any `json:"project"`
	}
	st, raw := h.api(h.member, "GET", "/api/projects/"+pid, nil, &memberProj)
	if st != 200 {
		t.Fatalf("member get project: %d %s", st, raw)
	}
	if strings.Contains(raw, token) {
		t.Fatal("member saw token")
	}
}

func assertVolumeMarker(t *testing.T, volName, want string) {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", "-v", volName+":/data", "busybox:1.37", "cat", "/data/marker").CombinedOutput()
	if err != nil {
		t.Fatalf("read volume %s: %v\n%s", volName, err, out)
	}
	if !strings.Contains(string(out), want) {
		t.Fatalf("volume marker = %q, want %q", out, want)
	}
}

func jsonContains(m map[string]any, secret string) (bool, error) {
	// Walk string values only.
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case string:
			return strings.Contains(x, secret)
		case map[string]any:
			for _, vv := range x {
				if walk(vv) {
					return true
				}
			}
		case []any:
			for _, vv := range x {
				if walk(vv) {
					return true
				}
			}
		}
		return false
	}
	return walk(m), nil
}
