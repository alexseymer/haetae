//go:build integration

package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type fakeOperatorSettings struct {
	mu  sync.Mutex
	val OperatorSettingsValue
	err error
}

func (f *fakeOperatorSettings) GetSettings(context.Context, uuid.UUID) (OperatorSettingsValue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.val, f.err
}
func (f *fakeOperatorSettings) SetSettings(_ context.Context, _ uuid.UUID, v OperatorSettingsValue) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.val = v
	return nil
}
func (f *fakeOperatorSettings) UsageToday(context.Context, uuid.UUID) (OperatorUsage, error) {
	return OperatorUsage{InputTokens: 12, OutputTokens: 3}, nil
}
func (f *fakeOperatorSettings) ProviderInfo() OperatorProvider {
	return OperatorProvider{Configured: true, Name: "anthropic", Local: false}
}
func (f *fakeOperatorSettings) Available(context.Context, uuid.UUID) (bool, string) {
	return false, "turned off"
}

type fakePusher struct {
	mu   sync.Mutex
	orgs []uuid.UUID
}

func (f *fakePusher) PushPolicy(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orgs = append(f.orgs, id)
}

func TestOperatorSettingsAPI(t *testing.T) {
	fs := &fakeOperatorSettings{val: OperatorSettingsValue{Autonomy: "report", DailyTokenCap: 200000}}
	fp := &fakePusher{}
	e := newEnv(t, func(c *Config) { c.OperatorSettings = fs; c.PolicyPusher = fp })
	org, admin := e.addOrgWithUser("op", "admin@op.test", "admin")
	_, member := e.addOrgWithUser("op2", "member@op.test", "member")
	anon := e.newClient()

	if s := anon.get("/api/settings/operator", nil); s != http.StatusUnauthorized {
		t.Fatalf("anon GET: %d", s)
	}
	if s, _, _ := anon.do(http.MethodPut, "/api/settings/operator", map[string]any{}); s != http.StatusUnauthorized {
		t.Fatalf("anon PUT: %d", s)
	}

	var got map[string]any
	if s := member.get("/api/settings/operator", &got); s != http.StatusOK {
		t.Fatalf("member GET: %d", s)
	}
	if got["autonomy"] != "report" || got["available"] != false || got["reason"] != "turned off" {
		t.Fatalf("GET body: %v", got)
	}
	prov := got["provider"].(map[string]any)
	use := got["usageToday"].(map[string]any)
	if prov["name"] != "anthropic" || prov["configured"] != true || prov["local"] != false || use["inputTokens"] != float64(12) {
		t.Fatalf("provider/usage: %v %v", prov, use)
	}

	body := map[string]any{"aiEnabled": true, "autonomy": "suggest", "dailyTokenCap": 1000}
	if s, _, _ := member.do(http.MethodPut, "/api/settings/operator", body); s != http.StatusForbidden {
		t.Fatalf("member PUT: %d", s)
	}
	for name, bad := range map[string]map[string]any{
		"autonomy":   {"aiEnabled": true, "autonomy": "root", "dailyTokenCap": 1},
		"cap low":    {"aiEnabled": true, "autonomy": "act", "dailyTokenCap": -1},
		"cap high":   {"aiEnabled": true, "autonomy": "act", "dailyTokenCap": 50_000_001},
		"missing":    {"autonomy": "act"},
		"wrong type": {"aiEnabled": "yes", "autonomy": "act", "dailyTokenCap": 1},
	} {
		if s, _, _ := admin.do(http.MethodPut, "/api/settings/operator", bad); s != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, s)
		}
	}
	if len(fp.orgs) != 0 || fs.val.Autonomy != "report" {
		t.Fatal("rejected requests changed state")
	}

	// CSRF header is required on writes.
	resp, err := admin.hc.Do(mustReq(t, e.ts.URL+"/api/settings/operator"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no CSRF header: %d", resp.StatusCode)
	}

	if s, _, _ := admin.do(http.MethodPut, "/api/settings/operator", body); s != http.StatusOK {
		t.Fatalf("admin PUT: %d", s)
	}
	if fs.val != (OperatorSettingsValue{AIEnabled: true, Autonomy: "suggest", DailyTokenCap: 1000}) {
		t.Fatalf("stored %+v", fs.val)
	}
	if len(fp.orgs) != 1 || fp.orgs[0] != org.ID {
		t.Fatalf("pushes %v", fp.orgs)
	}
	// Same autonomy again, other fields changed: no push.
	body["dailyTokenCap"] = 5
	if s, _, _ := admin.do(http.MethodPut, "/api/settings/operator", body); s != http.StatusOK {
		t.Fatal(s)
	}
	if len(fp.orgs) != 1 {
		t.Fatalf("pushed without an autonomy change: %v", fp.orgs)
	}
	body["autonomy"] = "act"
	_, _, _ = admin.do(http.MethodPut, "/api/settings/operator", body)
	if len(fp.orgs) != 2 {
		t.Fatalf("pushes %v", fp.orgs)
	}
}

func mustReq(t *testing.T, url string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPut, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
