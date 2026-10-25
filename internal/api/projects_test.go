//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/deploys"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/secrets"
	"github.com/alexseymer/haetae/internal/store"
)

type fakeAPISender struct {
	connected bool
	err       error
	result    *agentv1.CommandResult
	cmds      []*agentv1.Command
}

func (f *fakeAPISender) Connected(uuid.UUID) bool { return f.connected }

func (f *fakeAPISender) Send(_ context.Context, _ uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error) {
	f.cmds = append(f.cmds, cmd)
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &agentv1.CommandResult{}, nil
}

type projectsFixture struct {
	e      *env
	snd    *fakeAPISender
	org    store.Org
	host   store.Host
	admin  *client
	member *client
	other  *client // other organisation admin
	box    *secrets.Box
}

func newProjectsFixture(t *testing.T, connected bool) *projectsFixture {
	t.Helper()
	box, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snd := &fakeAPISender{connected: connected}
	var svc *deploys.Service
	e := newEnv(t, func(c *Config) {
		c.Secrets = box
		svc = &deploys.Service{Sender: snd, Box: box}
		c.Deploys = svc
	})
	svc.Store = e.st

	org, admin := e.addOrgWithUser("proj-org", "admin@proj.test", "admin")
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(context.Background(), org.ID, "member@proj.test", hash, "member"); err != nil {
		t.Fatal(err)
	}
	member := e.newClient()
	if s := member.post("/api/login", map[string]string{"email": "member@proj.test", "password": testPassword}, nil); s != http.StatusOK {
		t.Fatalf("member login %d", s)
	}
	_, other := e.addOrgWithUser("other-org", "admin@other.test", "admin")
	host := e.addHost(org.ID, "deploy-host")
	return &projectsFixture{e: e, snd: snd, org: org, host: host, admin: admin, member: member, other: other, box: box}
}

func (f *projectsFixture) createProject(t *testing.T, token string) map[string]any {
	t.Helper()
	body := map[string]any{
		"name": "shop",
		"source": map[string]any{
			"kind": "git", "gitUrl": "https://github.com/acme/shop", "gitRef": "main",
			"composePath": "compose.yaml", "gitToken": token,
		},
		"domain": "shop.example.com", "service": "web", "port": 8080, "autoDeploy": false,
	}
	var out map[string]any
	status, _, raw := f.admin.do(http.MethodPost, "/api/hosts/"+f.host.ID.String()+"/projects", body)
	if status != http.StatusCreated {
		t.Fatalf("create project: %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out["project"].(map[string]any)
}

func TestProjectsAdminVsMember(t *testing.T) {
	f := newProjectsFixture(t, true)
	body := map[string]any{
		"name": "shop",
		"source": map[string]any{
			"kind": "inline", "inlineCompose": "services:\n  web:\n    image: nginx\n",
		},
	}
	if s, _, _ := f.member.do(http.MethodPost, "/api/hosts/"+f.host.ID.String()+"/projects", body); s != http.StatusForbidden {
		t.Fatalf("member create: %d", s)
	}
	proj := f.createProject(t, "canary-token-xyz")
	pid := proj["id"].(string)
	if s := f.member.get("/api/projects/"+pid, nil); s != http.StatusOK {
		t.Fatalf("member get: %d", s)
	}
	if s, _, _ := f.member.do(http.MethodPost, "/api/projects/"+pid+"/deploy", map[string]any{}); s != http.StatusForbidden {
		t.Fatalf("member deploy: %d", s)
	}
}

func TestProjectsValidation(t *testing.T) {
	f := newProjectsFixture(t, true)
	for _, bad := range []string{"Shop", "shop;rm", "$(id)", "../x"} {
		body := map[string]any{
			"name": bad,
			"source": map[string]any{
				"kind": "inline", "inlineCompose": "services: {}\n",
			},
		}
		if s, _, raw := f.admin.do(http.MethodPost, "/api/hosts/"+f.host.ID.String()+"/projects", body); s != http.StatusBadRequest {
			t.Fatalf("name %q: %d %s", bad, s, raw)
		}
	}
	body := map[string]any{
		"name": "ok",
		"source": map[string]any{
			"kind": "git", "gitUrl": "https://github.com/acme/x;rm", "gitRef": "main",
		},
	}
	if s, _, _ := f.admin.do(http.MethodPost, "/api/hosts/"+f.host.ID.String()+"/projects", body); s != http.StatusBadRequest {
		t.Fatalf("bad url accepted")
	}
}

func TestProjectsTokenNeverReturned(t *testing.T) {
	f := newProjectsFixture(t, true)
	canary := "canary-secret-token-never-leak"
	proj := f.createProject(t, canary)
	pid := proj["id"].(string)
	paths := []string{
		"/api/projects",
		"/api/projects?hostId=" + f.host.ID.String(),
		"/api/projects/" + pid,
	}
	for _, path := range paths {
		status, _, raw := f.admin.do(http.MethodGet, path, nil)
		if status != http.StatusOK {
			t.Fatalf("%s: %d", path, status)
		}
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatalf("token leaked in %s: %s", path, raw)
		}
		if !bytes.Contains(raw, []byte(`"hasToken":true`)) && !bytes.Contains(raw, []byte(`"hasToken": true`)) {
			// json.Encoder may not space
			var check struct {
				Project  map[string]any   `json:"project"`
				Projects []map[string]any `json:"projects"`
			}
			_ = json.Unmarshal(raw, &check)
			has := false
			if check.Project != nil {
				has, _ = check.Project["hasToken"].(bool)
			}
			for _, p := range check.Projects {
				if p["id"] == pid {
					has, _ = p["hasToken"].(bool)
				}
			}
			if !has {
				t.Fatalf("hasToken missing in %s: %s", path, raw)
			}
		}
	}
}

func TestProjectsCrossOrg404(t *testing.T) {
	f := newProjectsFixture(t, true)
	proj := f.createProject(t, "")
	pid := proj["id"].(string)
	for _, path := range []string{
		"/api/projects/" + pid,
		"/api/projects/" + pid + "/deploys",
		"/api/projects/" + pid + "/deploy",
	} {
		method := http.MethodGet
		var body any
		if strings.HasSuffix(path, "/deploy") {
			method = http.MethodPost
			body = map[string]any{}
		}
		s, _, _ := f.other.do(method, path, body)
		if s != http.StatusNotFound {
			t.Fatalf("%s: %d", path, s)
		}
	}
}

func TestProjectsDeployBusyOffline(t *testing.T) {
	f := newProjectsFixture(t, false)
	proj := f.createProject(t, "")
	pid := proj["id"].(string)
	s, _, raw := f.admin.do(http.MethodPost, "/api/projects/"+pid+"/deploy", map[string]any{})
	if s != http.StatusServiceUnavailable {
		t.Fatalf("offline: %d %s", s, raw)
	}

	f2 := newProjectsFixture(t, true)
	proj2 := f2.createProject(t, "")
	pid2 := proj2["id"].(string)
	if s, _, _ := f2.admin.do(http.MethodPost, "/api/projects/"+pid2+"/deploy", map[string]any{}); s != http.StatusAccepted {
		t.Fatalf("first deploy: %d", s)
	}
	if s, _, _ := f2.admin.do(http.MethodPost, "/api/projects/"+pid2+"/deploy", map[string]any{}); s != http.StatusConflict {
		t.Fatalf("busy: %d", s)
	}
}

func TestProjectsLogsPagination(t *testing.T) {
	f := newProjectsFixture(t, true)
	proj := f.createProject(t, "")
	pid := proj["id"].(string)
	s, _, raw := f.admin.do(http.MethodPost, "/api/projects/"+pid+"/deploy", map[string]any{})
	if s != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", s, raw)
	}
	var started map[string]any
	_ = json.Unmarshal(raw, &started)
	did := started["deploy"].(map[string]any)["id"].(string)
	deployID, _ := uuid.Parse(did)

	for i := 1; i <= 5; i++ {
		if err := f.e.st.AppendDeployLog(context.Background(), f.org.ID, f.host.ID, deployID, store.DeployLogChunk{
			Seq: i, Stream: "stdout", Text: "line",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var page map[string]any
	if s := f.admin.get("/api/deploys/"+did+"/logs?after=2&limit=2", &page); s != http.StatusOK {
		t.Fatalf("logs: %d", s)
	}
	logs := page["logs"].([]any)
	if len(logs) != 2 {
		t.Fatalf("want 2 logs, got %v", logs)
	}
	if int(logs[0].(map[string]any)["seq"].(float64)) != 3 {
		t.Fatalf("first seq = %v", logs[0])
	}
	// Cross-org.
	if s, _, _ := f.other.do(http.MethodGet, "/api/deploys/"+did+"/logs", nil); s != http.StatusNotFound {
		t.Fatalf("cross-org logs: %d", s)
	}
	if s, _, _ := f.other.do(http.MethodGet, "/api/deploys/"+did, nil); s != http.StatusNotFound {
		t.Fatalf("cross-org deploy: %d", s)
	}
}

func TestProjectsDuplicateName(t *testing.T) {
	f := newProjectsFixture(t, true)
	_ = f.createProject(t, "")
	body := map[string]any{
		"name": "shop",
		"source": map[string]any{
			"kind": "inline", "inlineCompose": "services: {}\n",
		},
	}
	if s, _, _ := f.admin.do(http.MethodPost, "/api/hosts/"+f.host.ID.String()+"/projects", body); s != http.StatusConflict {
		t.Fatalf("dup: %d", s)
	}
}
