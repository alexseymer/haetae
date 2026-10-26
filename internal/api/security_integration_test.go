//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/deploys"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

type securityFixture struct {
	e      *env
	snd    *fakeAPISender
	org    store.Org
	host   store.Host
	admin  *client
	member *client
	other  *client
	otherH store.Host
}

func newSecurityFixture(t *testing.T, connected bool) *securityFixture {
	t.Helper()
	snd := &fakeAPISender{connected: connected}
	e := newEnv(t, func(c *Config) {
		c.Sender = snd
		c.Deploys = &deploys.Service{Sender: snd}
	})
	org, admin := e.addOrgWithUser("sec-api-org", "admin@secapi.test", "admin")
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(context.Background(), org.ID, "member@secapi.test", hash, "member"); err != nil {
		t.Fatal(err)
	}
	member := e.newClient()
	if s := member.post("/api/login", map[string]string{"email": "member@secapi.test", "password": testPassword}, nil); s != http.StatusOK {
		t.Fatalf("member login %d", s)
	}
	otherOrg, other := e.addOrgWithUser("sec-other", "admin@secother.test", "admin")
	host := e.addHost(org.ID, "sec-host")
	otherHost := e.addHost(otherOrg.ID, "other-sec-host")

	ctx := context.Background()
	_ = e.st.ReplaceContainers(ctx, org.ID, host.ID, []store.Container{
		{ContainerID: "ctr1", Name: "web", Image: "nginx:1", State: "running"},
		{ContainerID: "ctr2", Name: "db", Image: "postgres:16", State: "running"},
	}, true)

	return &securityFixture{
		e: e, snd: snd, org: org, host: host, admin: admin, member: member,
		other: other, otherH: otherHost,
	}
}

func TestSecurityEndpointsAdminMemberCrossOrg(t *testing.T) {
	f := newSecurityFixture(t, true)
	ctx := context.Background()
	path := "/api/hosts/" + f.host.ID.String()

	_ = f.e.st.UpsertHostSecurity(ctx, store.HostSecurity{
		HostID: f.host.ID, OrgID: f.org.ID,
		ConntrackState: "degraded", ConntrackReason: "permission denied",
		NotMonitored: []string{"ctr2"},
	})
	_ = f.e.st.ReplaceMisconfigs(ctx, f.org.ID, f.host.ID, []store.Misconfig{
		{ContainerID: "ctr1", Kind: "privileged", Severity: "high", Detail: "privileged=true"},
		{ContainerID: "ctr1", Kind: "root_user", Severity: "low", Detail: "user=0"},
	})
	_ = f.e.st.UpsertOutbound(ctx, f.org.ID, f.host.ID, []store.OutboundConn{
		{ContainerID: "ctr1", DstIP: "1.2.3.4", DstPort: 443, Proto: "tcp", Count: 3},
	})
	_ = f.e.st.UpsertImageUpdate(ctx, store.ImageUpdate{
		OrgID: f.org.ID, HostID: f.host.ID, ContainerID: "ctr1",
		Image: "nginx:1", Decision: "wait", Reason: "candidate younger than 48h",
	})

	var sec map[string]any
	if s := f.member.get(path+"/security", &sec); s != http.StatusOK {
		t.Fatalf("member security: %d", s)
	}
	ct := sec["conntrack"].(map[string]any)
	if ct["state"] != "degraded" || ct["reason"] != "permission denied" {
		t.Fatalf("conntrack=%v", ct)
	}
	notMon := ct["notMonitored"].([]any)
	if len(notMon) != 1 || notMon[0] != "ctr2" {
		t.Fatalf("notMonitored=%v", notMon)
	}
	mis := sec["misconfigs"].([]any)
	if len(mis) != 2 {
		t.Fatalf("misconfigs=%d", len(mis))
	}

	if s := f.other.get(path+"/security", nil); s != http.StatusNotFound {
		t.Fatalf("cross-org security: %d", s)
	}

	var out map[string]any
	if s := f.admin.get(path+"/outbound?limit=10", &out); s != http.StatusOK {
		t.Fatalf("outbound: %d", s)
	}
	if len(out["conns"].([]any)) != 1 {
		t.Fatalf("conns=%v", out["conns"])
	}
	if s := f.other.get(path+"/outbound", nil); s != http.StatusNotFound {
		t.Fatalf("cross-org outbound: %d", s)
	}

	var ups map[string]any
	if s := f.member.get(path+"/image-updates", &ups); s != http.StatusOK {
		t.Fatalf("image-updates: %d", s)
	}
	if len(ups["updates"].([]any)) != 1 {
		t.Fatalf("updates=%v", ups)
	}

	if s := f.member.get("/api/settings/updates", &out); s != http.StatusOK || out["autoPull"] != false {
		t.Fatalf("get updates: %d %v", s, out)
	}
	if s, _, _ := f.member.do(http.MethodPut, "/api/settings/updates", map[string]any{"autoPull": true}); s != http.StatusForbidden {
		t.Fatalf("member put updates: %d", s)
	}
	if s := f.admin.postPut("/api/settings/updates", map[string]any{"autoPull": true}, &out); s != http.StatusOK || out["autoPull"] != true {
		t.Fatalf("admin put updates: %d %v", s, out)
	}
}

func (c *client) postPut(path string, body, out any) int {
	c.e.t.Helper()
	status, _, b := c.do(http.MethodPut, path, body)
	if out != nil && status < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			c.e.t.Fatalf("PUT %s: %v\n%s", path, err, b)
		}
	}
	return status
}

func TestVulnerabilitiesFailedScanNeverClean(t *testing.T) {
	f := newSecurityFixture(t, true)
	ctx := context.Background()
	path := "/api/hosts/" + f.host.ID.String() + "/vulnerabilities"

	finished := time.Now().UTC()
	_, err := f.e.st.RecordFailedScan(ctx, f.org.ID, store.FailedScanInput{
		HostID: f.host.ID, ContainerID: "ctr1", Image: "nginx:1", ImageID: "sha256:abc",
		Trigger: "forced", Status: store.ScanFailed, Error: "syft exited 1",
		FinishedAt: &finished,
	})
	if err != nil {
		t.Fatal(err)
	}

	var resp map[string]any
	if s := f.admin.get(path, &resp); s != http.StatusOK {
		t.Fatalf("vulns: %d", s)
	}
	containers := resp["containers"].([]any)
	var web map[string]any
	for _, c := range containers {
		m := c.(map[string]any)
		if m["containerId"] == "ctr1" {
			web = m
		}
	}
	if web == nil {
		t.Fatal("ctr1 missing")
	}
	scan := web["scan"].(map[string]any)
	if scan["status"] != "failed" {
		t.Fatalf("status=%v want failed", scan["status"])
	}
	counts := web["counts"].(map[string]any)
	findings := web["findings"].([]any)
	if len(findings) != 0 {
		t.Fatalf("failed scan must not return findings; got %v", findings)
	}
	if counts["critical"].(float64) != 0 || counts["high"].(float64) != 0 {
		t.Fatalf("failed scan must not look clean: counts=%v", counts)
	}
	// Explicitly never report as a successful empty inventory.
	if scan["status"] == "ok" || scan["status"] == "none" {
		t.Fatal("failed scan reported as clean/none")
	}

	if s := f.other.get(path, nil); s != http.StatusNotFound {
		t.Fatalf("cross-org vulns: %d", s)
	}
}

func TestVulnerabilitiesNoneVsOK(t *testing.T) {
	f := newSecurityFixture(t, true)
	ctx := context.Background()
	path := "/api/hosts/" + f.host.ID.String() + "/vulnerabilities"

	finished := time.Now().UTC()
	_, err := f.e.st.SaveSbom(ctx, f.org.ID, store.SaveSbomInput{
		HostID: f.host.ID, ContainerID: "ctr1", Image: "nginx:1", ImageID: "sha256:ok1",
		Trigger: "start", ToolVersion: "syft@1", FinishedAt: &finished,
		Packages: []store.SbomPackage{{Name: "openssl", Version: "1.0", Type: "deb"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.st.SetMatchState(ctx, f.org.ID, "sha256:ok1", store.MatchOK, "", &finished); err != nil {
		t.Fatal(err)
	}
	if err := f.e.st.ReplaceFindings(ctx, f.org.ID, "sha256:ok1", []store.Finding{
		{
			VulnID: "CVE-2024-0001", Package: "openssl", Version: "1.0", FixedVersion: "1.1",
			Severity: store.FindingCritical, Description: "bad",
			URLs: []string{"https://nvd.nist.gov/vuln/detail/CVE-2024-0001", "http://evil.example/x"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var resp map[string]any
	if s := f.member.get(path, &resp); s != http.StatusOK {
		t.Fatalf("vulns: %d", s)
	}
	var web, dbc map[string]any
	for _, c := range resp["containers"].([]any) {
		m := c.(map[string]any)
		switch m["containerId"] {
		case "ctr1":
			web = m
		case "ctr2":
			dbc = m
		}
	}
	if web["scan"].(map[string]any)["status"] != "ok" {
		t.Fatalf("ctr1 scan=%v", web["scan"])
	}
	findings := web["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings=%v", findings)
	}
	urls := findings[0].(map[string]any)["urls"].([]any)
	if len(urls) != 1 || urls[0] != "https://nvd.nist.gov/vuln/detail/CVE-2024-0001" {
		t.Fatalf("urls must be https-only: %v", urls)
	}
	if dbc["scan"].(map[string]any)["status"] != "none" {
		t.Fatalf("unscanned must be none, got %v", dbc["scan"])
	}

	var sec map[string]any
	if s := f.admin.get("/api/hosts/"+f.host.ID.String()+"/security", &sec); s != http.StatusOK {
		t.Fatalf("security: %d", s)
	}
	sum := sec["summary"].(map[string]any)
	if sum["critical"].(float64) != 1 {
		t.Fatalf("summary critical=%v", sum["critical"])
	}
	if sec["scanFailed"] == true {
		t.Fatal("should not be scanFailed")
	}
}

func TestForceScanAdminCSRFRateLimit(t *testing.T) {
	f := newSecurityFixture(t, true)
	path := "/api/hosts/" + f.host.ID.String() + "/containers/ctr1/scan"

	if s, _, _ := f.member.do(http.MethodPost, path, map[string]any{}); s != http.StatusForbidden {
		t.Fatalf("member scan: %d", s)
	}

	// CSRF: missing header
	reqStatus := func() int {
		req, _ := http.NewRequest(http.MethodPost, f.e.ts.URL+path, nil)
		req.Header.Set("Content-Type", "application/json")
		// copy cookies from admin
		for _, c := range f.admin.hc.Jar.Cookies(req.URL) {
			req.AddCookie(c)
		}
		resp, err := f.admin.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if s := reqStatus(); s != http.StatusForbidden {
		t.Fatalf("no CSRF: %d", s)
	}

	if s, _, raw := f.admin.do(http.MethodPost, path, map[string]any{}); s != http.StatusAccepted {
		t.Fatalf("admin scan: %d %s", s, raw)
	}
	if len(f.snd.cmds) != 1 || f.snd.cmds[0].GetScanContainer() == nil {
		t.Fatalf("cmds=%v", f.snd.cmds)
	}
	if f.snd.cmds[0].GetScanContainer().GetContainerId() != "ctr1" {
		t.Fatalf("container=%v", f.snd.cmds[0].GetScanContainer())
	}

	if s, _, _ := f.admin.do(http.MethodPost, path, map[string]any{}); s != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d", s)
	}

	// Cross-org host
	otherPath := "/api/hosts/" + f.otherH.ID.String() + "/containers/ctr1/scan"
	if s, _, _ := f.admin.do(http.MethodPost, otherPath, map[string]any{}); s != http.StatusNotFound {
		t.Fatalf("cross-org scan: %d", s)
	}
}

func TestForceScanHostOffline(t *testing.T) {
	f := newSecurityFixture(t, false)
	path := "/api/hosts/" + f.host.ID.String() + "/containers/ctr1/scan"
	if s, _, _ := f.admin.do(http.MethodPost, path, map[string]any{}); s != http.StatusServiceUnavailable {
		t.Fatalf("offline: %d", s)
	}
}

func TestForceScanAgentError(t *testing.T) {
	f := newSecurityFixture(t, true)
	f.snd.result = &agentv1.CommandResult{Error: "container not found"}
	path := "/api/hosts/" + f.host.ID.String() + "/containers/missing/scan"
	// container must belong — seed won't have "missing"
	if s, _, _ := f.admin.do(http.MethodPost, path, map[string]any{}); s != http.StatusNotFound {
		t.Fatalf("missing container: %d", s)
	}
}
