//go:build integration

package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type fakeDisconnecter struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (f *fakeDisconnecter) DisconnectHost(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	return true
}

func TestRemoveHost(t *testing.T) {
	disc := &fakeDisconnecter{}
	var logs bytes.Buffer
	e := newEnv(t, func(c *Config) {
		c.HostDisconnecter = disc
		c.Log = slog.New(slog.NewTextHandler(&logs, nil))
	})
	org, admin := e.addOrgWithUser("rm-org", "admin@rm.test", "admin")
	_, other := e.addOrgWithUser("rm-other", "admin@rm-other.test", "admin")
	signedOut := e.newClient()
	h := e.addHost(org.ID, "doomed")
	keep := e.addHost(org.ID, "keeper")
	path := "/api/hosts/" + h.ID.String()

	// Other orgs and signed-out callers cannot remove it; nothing changes.
	if s, _, _ := other.do(http.MethodDelete, path, map[string]any{}); s != http.StatusNotFound {
		t.Fatalf("cross-org: %d", s)
	}
	if s, _, _ := signedOut.do(http.MethodDelete, path, map[string]any{}); s != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", s)
	}
	if len(disc.ids) != 0 {
		t.Fatal("disconnect called for rejected requests")
	}
	// CSRF header is required like on every mutating endpoint.
	req, _ := http.NewRequest(http.MethodDelete, e.ts.URL+path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range admin.hc.Jar.Cookies(req.URL) {
		req.AddCookie(ck)
	}
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("DELETE without the request header: %v %v", resp, err)
	}

	if s, _, raw := admin.do(http.MethodDelete, path, map[string]any{}); s != http.StatusNoContent {
		t.Fatalf("remove: %d %s", s, raw)
	}
	if len(disc.ids) != 1 || disc.ids[0] != h.ID {
		t.Fatalf("disconnected: %v", disc.ids)
	}
	au, err := e.st.UserByEmail(context.Background(), "admin@rm.test")
	if err != nil {
		t.Fatal(err)
	}
	if line := logs.String(); !strings.Contains(line, "audit: host removed") || !strings.Contains(line, "user="+au.ID.String()) {
		t.Fatalf("audit line missing actor:\n%s", line)
	}

	// Gone from the list and every host-scoped endpoint; the other host stays.
	var list struct {
		Hosts []struct{ Name string } `json:"hosts"`
	}
	if s := admin.get("/api/hosts", &list); s != http.StatusOK || len(list.Hosts) != 1 || list.Hosts[0].Name != "keeper" {
		t.Fatalf("list: %d %+v", s, list)
	}
	for _, p := range []string{"", "/containers", "/metrics", "/containers/abc/metrics", "/security", "/vulnerabilities", "/logs", "/outbound", "/image-updates"} {
		if s, _, _ := admin.do(http.MethodGet, path+p, nil); s != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path+p, s)
		}
	}
	if s, _, _ := admin.do(http.MethodGet, "/api/hosts/"+keep.ID.String(), nil); s != http.StatusOK {
		t.Errorf("other host: %d", s)
	}
	// Removing twice is a 404; the name can be used for a new token.
	if s, _, _ := admin.do(http.MethodDelete, path, map[string]any{}); s != http.StatusNotFound {
		t.Fatalf("second remove: %d", s)
	}
	if s := admin.post("/api/hosts/enrollment", map[string]any{"name": "doomed", "ttlHours": 1}, nil); s != http.StatusCreated {
		t.Fatalf("enrollment for the freed name: %d", s)
	}
}

func TestRemoveHostAdminOnly(t *testing.T) {
	f := newChatFixture(t) // has an admin and a member in one org
	h := f.e.addHost(f.org.ID, "member-cannot")
	if s, _, _ := f.member.do(http.MethodDelete, "/api/hosts/"+h.ID.String(), map[string]any{}); s != http.StatusForbidden {
		t.Fatalf("member: %d", s)
	}
	if s := f.admin.get("/api/hosts/"+h.ID.String(), nil); s != http.StatusOK {
		t.Fatalf("host gone after a member's attempt: %d", s)
	}
}
