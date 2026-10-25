//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/store"
)

func TestDataHostsList(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	orgA, cA := e.addOrgWithUser("hosts-a", "a@hosts.test", "admin")
	orgB, cB := e.addOrgWithUser("hosts-b", "b@hosts.test", "admin")

	h1 := e.addHost(orgA.ID, "alpha")
	h2 := e.addHost(orgA.ID, "bravo")
	_ = e.addHost(orgB.ID, "other")

	created := time.Now().UTC().Add(-time.Minute)
	started := created.Add(time.Second)
	if err := e.st.ReplaceContainers(ctx, orgA.ID, h1.ID, []store.Container{
		{ContainerID: "c1", Name: "web", State: "running", CreatedAt: &created, StartedAt: &started},
		{ContainerID: "c2", Name: "db", State: "exited"},
		{ContainerID: "c3", Name: "cache", State: "running"},
	}, true); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ReplaceContainers(ctx, orgA.ID, h2.ID, []store.Container{
		{ContainerID: "x1", Name: "one", State: "created"},
	}, true); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Hosts []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Hostname   string `json:"hostname"`
			Containers struct {
				Running int `json:"running"`
				Total   int `json:"total"`
			} `json:"containers"`
		} `json:"hosts"`
	}
	if s := cA.get("/api/hosts", &out); s != http.StatusOK {
		t.Fatalf("status %d", s)
	}
	if len(out.Hosts) != 2 {
		t.Fatalf("want 2 hosts, got %d", len(out.Hosts))
	}
	if out.Hosts[0].Name != "alpha" || out.Hosts[1].Name != "bravo" {
		t.Fatalf("order: %+v", out.Hosts)
	}
	if out.Hosts[0].ID != h1.ID.String() || out.Hosts[0].Containers.Running != 2 || out.Hosts[0].Containers.Total != 3 {
		t.Fatalf("alpha: %+v", out.Hosts[0])
	}
	if out.Hosts[1].Containers.Running != 0 || out.Hosts[1].Containers.Total != 1 {
		t.Fatalf("bravo: %+v", out.Hosts[1])
	}

	var outB struct {
		Hosts []struct {
			Name string `json:"name"`
		} `json:"hosts"`
	}
	if s := cB.get("/api/hosts", &outB); s != http.StatusOK {
		t.Fatalf("org B status %d", s)
	}
	if len(outB.Hosts) != 1 || outB.Hosts[0].Name != "other" {
		t.Fatalf("org B hosts: %+v", outB.Hosts)
	}
}

func TestDataCrossOrganisation(t *testing.T) {
	e := newEnv(t)
	orgA, cA := e.addOrgWithUser("cross-a", "a@cross.test", "admin")
	_, cB := e.addOrgWithUser("cross-b", "b@cross.test", "admin")
	h := e.addHost(orgA.ID, "secret")
	id := h.ID.String()

	paths := []string{
		"/api/hosts/" + id,
		"/api/hosts/" + id + "/containers",
		"/api/hosts/" + id + "/metrics?metric=cpu_pct",
		"/api/hosts/" + id + "/containers/abc/metrics?metric=cpu_pct",
		"/api/hosts/" + id + "/logs",
	}
	for _, path := range paths {
		if s := cB.get(path, nil); s != http.StatusNotFound {
			t.Errorf("org B %s: got %d want 404", path, s)
		}
		if s := cA.get(path, nil); s != http.StatusOK {
			t.Errorf("org A %s: got %d want 200", path, s)
		}
	}
	if s := cB.get("/api/hosts/not-a-uuid", nil); s != http.StatusNotFound {
		t.Errorf("bad uuid: got %d", s)
	}
}

func TestDataMetrics(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org, c := e.addOrgWithUser("metrics-org", "m@metrics.test", "admin")
	h := e.addHost(org.ID, "mhost")

	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-20 * time.Minute)
	if err := e.st.InsertHostMetrics(ctx, org.ID, h.ID, []store.HostMetric{
		{TS: base, CPUPct: 11.5, Load1: 0.5, MemTotal: 1000, MemAvail: 400, DiskTotal: 5000, DiskUsed: 1000, NetRxBps: 1, NetTxBps: 2},
		{TS: base.Add(10 * time.Second), CPUPct: 22.5, Load1: 0.6, MemTotal: 1000, MemAvail: 300, DiskTotal: 5000, DiskUsed: 1100, NetRxBps: 3, NetTxBps: 4},
	}); err != nil {
		t.Fatal(err)
	}

	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	path := "/api/hosts/" + h.ID.String() + "/metrics?metric=cpu_pct&from=" + from + "&to=" + to
	var out struct {
		Metric     string `json:"metric"`
		Resolution string `json:"resolution"`
		Points     []struct {
			T   int64   `json:"t"`
			V   float64 `json:"v"`
			Max float64 `json:"max"`
		} `json:"points"`
	}
	if s := c.get(path, &out); s != http.StatusOK {
		t.Fatalf("status %d", s)
	}
	if out.Metric != "cpu_pct" || out.Resolution != "raw" {
		t.Fatalf("got metric=%s resolution=%s", out.Metric, out.Resolution)
	}
	if len(out.Points) != 2 {
		t.Fatalf("points: %+v", out.Points)
	}
	if out.Points[0].T != base.UnixMilli() || out.Points[0].V != 11.5 {
		t.Fatalf("first point: %+v", out.Points[0])
	}

	// Longer span picks 1m (or 1h) rather than raw.
	fromLong := now.Add(-8 * time.Hour).Format(time.RFC3339)
	pathLong := "/api/hosts/" + h.ID.String() + "/metrics?metric=cpu_pct&from=" + fromLong + "&to=" + to
	var longOut struct {
		Resolution string `json:"resolution"`
	}
	if s := c.get(pathLong, &longOut); s != http.StatusOK {
		t.Fatalf("long status %d", s)
	}
	if longOut.Resolution == "raw" {
		t.Fatalf("expected coarser resolution, got %s", longOut.Resolution)
	}

	bad := []string{
		"/api/hosts/" + h.ID.String() + "/metrics?metric=nope",
		"/api/hosts/" + h.ID.String() + "/metrics?metric=cpu_pct&from=nope&to=" + to,
		"/api/hosts/" + h.ID.String() + "/metrics?metric=cpu_pct&from=" + to + "&to=" + from,
		"/api/hosts/" + h.ID.String() + "/metrics?metric=cpu_pct&from=" + now.Add(-500*24*time.Hour).Format(time.RFC3339) + "&to=" + to,
	}
	for _, p := range bad {
		if s := c.get(p, nil); s != http.StatusBadRequest {
			t.Errorf("%s: got %d want 400", p, s)
		}
	}

	// Unix milliseconds accepted.
	msPath := "/api/hosts/" + h.ID.String() + "/metrics?metric=cpu_pct&from=" +
		strconv.FormatInt(base.Add(-time.Minute).UnixMilli(), 10) + "&to=" +
		strconv.FormatInt(now.UnixMilli(), 10)
	if s := c.get(msPath, &out); s != http.StatusOK {
		t.Fatalf("unix ms status %d", s)
	}
}

func TestDataContainerMetrics(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org, c := e.addOrgWithUser("cmetrics-org", "cm@metrics.test", "admin")
	h := e.addHost(org.ID, "chost")
	cid := "container-abcdef"

	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-15 * time.Minute)
	if err := e.st.InsertContainerMetrics(ctx, org.ID, h.ID, []store.ContainerMetric{
		{ContainerID: cid, TS: base, CPUPct: 5, MemUsage: 100, MemLimit: 200, NetRxBps: 1, NetTxBps: 2, BlkReadBps: 3, BlkWriteBps: 4, Pids: 7},
		{ContainerID: cid, TS: base.Add(5 * time.Second), CPUPct: 9, MemUsage: 110, MemLimit: 200, NetRxBps: 1, NetTxBps: 2, BlkReadBps: 3, BlkWriteBps: 4, Pids: 8},
	}); err != nil {
		t.Fatal(err)
	}

	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	path := "/api/hosts/" + h.ID.String() + "/containers/" + cid + "/metrics?metric=cpu_pct&from=" + from + "&to=" + to
	var out struct {
		Metric     string `json:"metric"`
		Resolution string `json:"resolution"`
		Points     []struct {
			T int64   `json:"t"`
			V float64 `json:"v"`
		} `json:"points"`
	}
	if s := c.get(path, &out); s != http.StatusOK {
		t.Fatalf("status %d", s)
	}
	if out.Resolution != "raw" || len(out.Points) != 2 || out.Points[0].V != 5 {
		t.Fatalf("got %+v", out)
	}

	if s := c.get("/api/hosts/"+h.ID.String()+"/containers/"+cid+"/metrics?metric=bogus", nil); s != http.StatusBadRequest {
		t.Errorf("bad metric: %d", s)
	}
	longCID := strings.Repeat("a", 129)
	if s := c.get("/api/hosts/"+h.ID.String()+"/containers/"+longCID+"/metrics?metric=cpu_pct", nil); s != http.StatusNotFound {
		t.Errorf("long cid: %d", s)
	}
}

func TestDataLogs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org, c := e.addOrgWithUser("logs-org", "l@logs.test", "admin")
	h := e.addHost(org.ID, "lhost")

	now := time.Now().UTC().Truncate(time.Second)
	evs := make([]store.LogEvent, 0, 12)
	for i := 0; i < 6; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute)
		evs = append(evs, store.LogEvent{
			ContainerID: "c1", TemplateID: "t-info-" + strconv.Itoa(i), Level: store.LevelInfo,
			Template: "info", Sample: "info " + strconv.Itoa(i), Count: 1, FirstTS: ts, LastTS: ts,
		})
		evs = append(evs, store.LogEvent{
			ContainerID: "c1", TemplateID: "t-warn-" + strconv.Itoa(i), Level: store.LevelWarn,
			Template: "warn", Sample: "warn " + strconv.Itoa(i), Count: 1, FirstTS: ts, LastTS: ts,
		})
	}
	if err := e.st.InsertLogEvents(ctx, org.ID, h.ID, evs); err != nil {
		t.Fatal(err)
	}

	var warnOut struct {
		Events []struct {
			Level    string `json:"level"`
			Sample   string `json:"sample"`
			LastSeen string `json:"lastSeen"`
		} `json:"events"`
	}
	if s := c.get("/api/hosts/"+h.ID.String()+"/logs", &warnOut); s != http.StatusOK {
		t.Fatalf("default status %d", s)
	}
	if len(warnOut.Events) != 6 {
		t.Fatalf("default minLevel=warn: got %d events", len(warnOut.Events))
	}
	for i, ev := range warnOut.Events {
		if ev.Level != "warn" {
			t.Fatalf("event %d level %s", i, ev.Level)
		}
		if i > 0 && warnOut.Events[i-1].LastSeen < ev.LastSeen {
			t.Fatalf("not newest first: %+v", warnOut.Events)
		}
	}

	var allOut struct {
		Events []struct{} `json:"events"`
	}
	if s := c.get("/api/hosts/"+h.ID.String()+"/logs?minLevel=all", &allOut); s != http.StatusOK {
		t.Fatalf("all status %d", s)
	}
	if len(allOut.Events) != 12 {
		t.Fatalf("minLevel=all: got %d", len(allOut.Events))
	}

	// Cap limit at 500.
	many := make([]store.LogEvent, 0, 520)
	for i := 0; i < 520; i++ {
		ts := now.Add(-time.Duration(i) * time.Second)
		many = append(many, store.LogEvent{
			ContainerID: "c1", TemplateID: "cap-" + strconv.Itoa(i), Level: store.LevelError,
			Template: "e", Sample: "e", Count: 1, FirstTS: ts, LastTS: ts,
		})
	}
	if err := e.st.InsertLogEvents(ctx, org.ID, h.ID, many); err != nil {
		t.Fatal(err)
	}
	var capped struct {
		Events []struct{} `json:"events"`
	}
	if s := c.get("/api/hosts/"+h.ID.String()+"/logs?minLevel=error&limit=100000", &capped); s != http.StatusOK {
		t.Fatalf("cap status %d", s)
	}
	if len(capped.Events) != 500 {
		t.Fatalf("limit cap: got %d want 500", len(capped.Events))
	}
}

func TestDataEnrollment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org, admin := e.addOrgWithUser("enroll-org", "admin@enroll.test", "admin")

	pwHash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(ctx, org.ID, "member@enroll.test", pwHash, "member"); err != nil {
		t.Fatal(err)
	}
	member := e.newClient()
	if s := member.post("/api/login", map[string]string{"email": "member@enroll.test", "password": testPassword}, nil); s != http.StatusOK {
		t.Fatalf("member login: %d", s)
	}

	var created struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
		Command   string `json:"command"`
		Host      string `json:"host"`
	}
	if s := admin.post("/api/hosts/enrollment", map[string]any{"name": "newhost", "ttlHours": 12}, &created); s != http.StatusCreated {
		t.Fatalf("admin enroll: %d", s)
	}
	if created.Token == "" || created.Host != "newhost" || created.ExpiresAt == "" {
		t.Fatalf("response: %+v", created)
	}
	for _, pin := range []string{created.Token, e.api.cfg.CAPin, e.api.cfg.WebPin, e.api.cfg.GatewayURL} {
		if !strings.Contains(created.Command, pin) {
			t.Errorf("command missing %q:\n%s", pin, created.Command)
		}
	}
	if _, err := e.st.EnrollHost(ctx, created.Token, store.HostInfo{Hostname: "newhost.example"},
		func(uuid.UUID) (string, error) { return "serial-new", nil }); err != nil {
		t.Fatalf("EnrollHost with returned token: %v", err)
	}

	if s := member.post("/api/hosts/enrollment", map[string]any{"name": "other"}, nil); s != http.StatusForbidden {
		t.Fatalf("member: got %d want 403", s)
	}

	_ = e.addHost(org.ID, "taken")
	if s := admin.post("/api/hosts/enrollment", map[string]any{"name": "taken"}, nil); s != http.StatusConflict {
		t.Fatalf("duplicate: got %d want 409", s)
	}

	for _, body := range []map[string]any{
		{"name": "-bad"},
		{"name": "ok", "ttlHours": 0},
		{"name": "ok", "ttlHours": 200},
		{"name": ""},
	} {
		if s := admin.post("/api/hosts/enrollment", body, nil); s != http.StatusBadRequest {
			t.Errorf("body %+v: got %d want 400", body, s)
		}
	}
}

func TestDataUnauthenticated(t *testing.T) {
	e := newEnv(t)
	org, _ := e.addOrgWithUser("unauth-org", "u@unauth.test", "admin")
	h := e.addHost(org.ID, "uhost")
	id := h.ID.String()
	anon := e.newClient()

	gets := []string{
		"/api/hosts",
		"/api/hosts/" + id,
		"/api/hosts/" + id + "/containers",
		"/api/hosts/" + id + "/metrics?metric=cpu_pct",
		"/api/hosts/" + id + "/containers/c1/metrics?metric=cpu_pct",
		"/api/hosts/" + id + "/logs",
	}
	for _, path := range gets {
		if s := anon.get(path, nil); s != http.StatusUnauthorized {
			t.Errorf("GET %s: got %d want 401", path, s)
		}
	}
	if s := anon.post("/api/hosts/enrollment", map[string]any{"name": "x"}, nil); s != http.StatusUnauthorized {
		t.Errorf("POST enrollment: got %d want 401", s)
	}

	// CSRF: authenticated POST without X-Haetae-Request.
	_, admin := e.addOrgWithUser("csrf-org", "csrf@unauth.test", "admin")
	body, _ := json.Marshal(map[string]any{"name": "csrfhost"})
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/hosts/enrollment", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// cookies from jar, but no X-Haetae-Request
	resp, err := admin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF header: got %d want 403", resp.StatusCode)
	}
}
