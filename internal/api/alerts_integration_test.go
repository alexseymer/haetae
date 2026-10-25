//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/store"
)

type alertsFixture struct {
	e         *env
	org       store.Org
	host      store.Host
	admin     *client
	member    *client
	other     *client
	otherOrg  store.Org
	otherHost store.Host
}

func newAlertsFixture(t *testing.T) *alertsFixture {
	t.Helper()
	e := newEnv(t)
	org, admin := e.addOrgWithUser("alerts-org", "admin@alerts.test", "admin")
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(context.Background(), org.ID, "member@alerts.test", hash, "member"); err != nil {
		t.Fatal(err)
	}
	member := e.newClient()
	if s := member.post("/api/login", map[string]string{"email": "member@alerts.test", "password": testPassword}, nil); s != http.StatusOK {
		t.Fatalf("member login %d", s)
	}
	otherOrg, other := e.addOrgWithUser("other-alerts", "admin@other-alerts.test", "admin")
	host := e.addHost(org.ID, "alert-host")
	otherHost := e.addHost(otherOrg.ID, "other-host")
	return &alertsFixture{
		e: e, org: org, host: host, admin: admin, member: member,
		other: other, otherOrg: otherOrg, otherHost: otherHost,
	}
}

func (f *alertsFixture) seedIncident(t *testing.T, orgID, hostID uuid.UUID, title string) (store.Incident, store.Alert) {
	t.Helper()
	ctx := context.Background()
	inc, err := f.e.st.InsertIncidentForTest(ctx, store.Incident{
		OrgID: orgID, HostID: hostID, Title: title,
		Severity: store.SeverityHigh, State: store.IncidentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	al, err := f.e.st.InsertAlertForTest(ctx, store.Alert{
		OrgID: orgID, HostID: hostID, IncidentID: inc.ID,
		Rule: store.RuleOOMKill, Severity: store.SeverityHigh,
		State: store.AlertFiring, DedupeKey: "oom|" + hostID.String() + "|" + uuid.New().String(),
		Summary: "out of memory", Evidence: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return inc, al
}

func TestIncidentsListAndGet(t *testing.T) {
	f := newAlertsFixture(t)
	inc, _ := f.seedIncident(t, f.org.ID, f.host.ID, "shop: oom")
	otherInc, _ := f.seedIncident(t, f.otherOrg.ID, f.otherHost.ID, "other")

	var list struct {
		Incidents []map[string]any `json:"incidents"`
	}
	if s := f.admin.get("/api/incidents?state=open", &list); s != http.StatusOK {
		t.Fatalf("list: %d", s)
	}
	if len(list.Incidents) != 1 {
		t.Fatalf("want 1 incident, got %d", len(list.Incidents))
	}
	if list.Incidents[0]["id"] != inc.ID.String() {
		t.Fatalf("id=%v", list.Incidents[0]["id"])
	}
	if list.Incidents[0]["hostName"] != f.host.Name {
		t.Fatalf("hostName=%v", list.Incidents[0]["hostName"])
	}
	if list.Incidents[0]["alertCount"].(float64) != 1 {
		t.Fatalf("alertCount=%v", list.Incidents[0]["alertCount"])
	}

	if s := f.member.get("/api/incidents?hostId="+f.host.ID.String(), &list); s != http.StatusOK || len(list.Incidents) != 1 {
		t.Fatalf("member host filter: %d len=%d", s, len(list.Incidents))
	}
	if s := f.admin.get("/api/incidents?hostId="+f.otherHost.ID.String(), nil); s != http.StatusNotFound {
		t.Fatalf("cross-org hostId: %d", s)
	}

	var detail struct {
		Incident map[string]any   `json:"incident"`
		Alerts   []map[string]any `json:"alerts"`
	}
	if s := f.admin.get("/api/incidents/"+inc.ID.String(), &detail); s != http.StatusOK {
		t.Fatalf("get: %d", s)
	}
	if len(detail.Alerts) != 1 {
		t.Fatalf("alerts=%d", len(detail.Alerts))
	}
	if s := f.admin.get("/api/incidents/"+otherInc.ID.String(), nil); s != http.StatusNotFound {
		t.Fatalf("cross-org incident: %d", s)
	}
	if s := f.admin.get("/api/incidents/"+uuid.New().String(), nil); s != http.StatusNotFound {
		t.Fatalf("missing: %d", s)
	}
}

func TestAlertsList(t *testing.T) {
	f := newAlertsFixture(t)
	_, al := f.seedIncident(t, f.org.ID, f.host.ID, "x")
	var out struct {
		Alerts []map[string]any `json:"alerts"`
	}
	if s := f.member.get("/api/alerts?state=firing", &out); s != http.StatusOK {
		t.Fatalf("%d", s)
	}
	if len(out.Alerts) != 1 || out.Alerts[0]["id"] != al.ID.String() {
		t.Fatalf("%+v", out.Alerts)
	}
	if s := f.admin.get("/api/alerts?state=nope", nil); s != http.StatusBadRequest {
		t.Fatalf("bad state: %d", s)
	}
}

func TestAlertSettingsAdminMemberCSRFValidation(t *testing.T) {
	f := newAlertsFixture(t)
	var got map[string]any
	if s := f.member.get("/api/settings/alerts", &got); s != http.StatusOK {
		t.Fatalf("member get: %d", s)
	}
	if got["timeZone"] != "UTC" || got["briefingHour"].(float64) != 8 {
		t.Fatalf("defaults=%v", got)
	}

	body := map[string]any{
		"timeZone": "Europe/Berlin", "briefingHour": 7,
		"cpuPct": 80, "memPct": 85, "diskPct": 90,
		"sustainedS": 60, "offlineAfterS": 60,
	}
	if s, _, _ := f.member.do(http.MethodPut, "/api/settings/alerts", body); s != http.StatusForbidden {
		t.Fatalf("member put: %d", s)
	}
	// CSRF required on writes.
	req, _ := http.NewRequest(http.MethodPut, f.e.ts.URL+"/api/settings/alerts", bytes.NewReader(mustJSON(body)))
	req.Header.Set("Content-Type", "application/json")
	// cookie from admin jar
	for _, c := range f.admin.hc.Jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", resp.StatusCode)
	}

	if s, _, raw := f.admin.do(http.MethodPut, "/api/settings/alerts", body); s != http.StatusOK {
		t.Fatalf("admin put: %d %s", s, raw)
	}
	bad := map[string]any{
		"timeZone": "Nope/Zone", "briefingHour": 7,
		"cpuPct": 80, "memPct": 85, "diskPct": 90,
		"sustainedS": 60, "offlineAfterS": 60,
	}
	s, _, raw := f.admin.do(http.MethodPut, "/api/settings/alerts", bad)
	if s != http.StatusBadRequest {
		t.Fatalf("bad tz: %d %s", s, raw)
	}
	if !bytes.Contains(raw, []byte("time zone")) && !bytes.Contains(raw, []byte("timeZone")) && !bytes.Contains(raw, []byte("invalid")) {
		t.Fatalf("want validation error text, got %s", raw)
	}
}

func TestMutesAdminMemberCrossOrgCSRF(t *testing.T) {
	f := newAlertsFixture(t)
	body := map[string]any{"hostId": f.host.ID.String(), "rule": store.RuleOOMKill, "days": 7}
	if s, _, _ := f.member.do(http.MethodPost, "/api/mutes", body); s != http.StatusForbidden {
		t.Fatalf("member create: %d", s)
	}
	var created struct {
		Mute map[string]any `json:"mute"`
	}
	if s := f.admin.post("/api/mutes", body, &created); s != http.StatusCreated {
		t.Fatalf("create: %d", s)
	}
	mid := created.Mute["id"].(string)

	var list struct {
		Mutes []map[string]any `json:"mutes"`
	}
	if s := f.member.get("/api/mutes", &list); s != http.StatusOK || len(list.Mutes) != 1 {
		t.Fatalf("list active: %d len=%d", s, len(list.Mutes))
	}

	// Effect: mute exists and matches host/rule.
	mutes, err := f.e.st.ActiveMutes(context.Background(), f.org.ID, time.Now())
	if err != nil || len(mutes) != 1 || !store.MuteMatches(mutes[0], f.host.ID, store.RuleOOMKill) {
		t.Fatalf("mute effect: %+v %v", mutes, err)
	}

	otherMute, err := f.e.st.CreateMute(context.Background(), store.Mute{
		OrgID: f.otherOrg.ID, Until: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s, _, _ := f.admin.do(http.MethodDelete, "/api/mutes/"+otherMute.ID.String(), map[string]any{}); s != http.StatusNotFound {
		t.Fatalf("cross-org delete: %d", s)
	}
	if s, _, _ := f.member.do(http.MethodDelete, "/api/mutes/"+mid, map[string]any{}); s != http.StatusForbidden {
		t.Fatalf("member delete: %d", s)
	}
	if s, _, _ := f.admin.do(http.MethodDelete, "/api/mutes/"+mid, map[string]any{}); s != http.StatusNoContent {
		t.Fatalf("admin delete: %d", s)
	}

	if s, _, _ := f.admin.do(http.MethodPost, "/api/mutes", map[string]any{"days": 100}); s != http.StatusBadRequest {
		t.Fatalf("days validation: %d", s)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
