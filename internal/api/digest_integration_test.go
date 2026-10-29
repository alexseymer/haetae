//go:build integration

package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
)

// fakeDigester records the scopes the digest endpoints were called with.
type fakeDigester struct {
	mu        sync.Mutex
	logScopes []operator.LogScope
	secScopes []operator.SecurityScope
}

func (f *fakeDigester) SummarizeLogs(_ context.Context, _ uuid.UUID, scope operator.LogScope) (operator.Digest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logScopes = append(f.logScopes, scope)
	return operator.Digest{Kind: operator.DigestLogs, Title: "Log summary", Quiet: true}, nil
}

func (f *fakeDigester) SecurityDigest(_ context.Context, _ uuid.UUID, scope operator.SecurityScope) (operator.Digest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secScopes = append(f.secScopes, scope)
	return operator.Digest{Kind: operator.DigestSecurity, Title: "Security digest", Quiet: true}, nil
}

func TestDigestEndpointsAuthScopeAndValidation(t *testing.T) {
	fd := &fakeDigester{}
	e := newEnv(t, func(c *Config) { c.Digests = fd })
	orgA, member := e.addOrgWithUser("digest-a", "member@digest-a.test", "member")
	_, foreign := e.addOrgWithUser("digest-b", "member@digest-b.test", "member")
	hostA := e.addHost(orgA.ID, "hel1")
	anon := e.newClient()

	logsPath := "/api/hosts/" + hostA.ID.String() + "/logs/summary"
	if s, _, _ := anon.do(http.MethodPost, logsPath, map[string]any{}); s != http.StatusUnauthorized {
		t.Fatalf("anon logs summary: %d", s)
	}
	if s, _, _ := anon.do(http.MethodPost, "/api/security/digest", map[string]any{}); s != http.StatusUnauthorized {
		t.Fatalf("anon security digest: %d", s)
	}

	// Any organisation member may ask.
	var got map[string]any
	if s := member.post(logsPath, map[string]any{"window": "1h"}, &got); s != http.StatusOK {
		t.Fatalf("member logs summary: %d", s)
	}
	if got["kind"] != operator.DigestLogs {
		t.Fatalf("logs body: %v", got)
	}
	if len(fd.logScopes) != 1 || fd.logScopes[0].HostID != hostA.ID || fd.logScopes[0].Window != time.Hour {
		t.Fatalf("logs scope: %+v", fd.logScopes)
	}
	if s := member.post("/api/security/digest", map[string]any{"hostId": hostA.ID.String()}, nil); s != http.StatusOK {
		t.Fatalf("member security digest: %d", s)
	}
	if s := member.post("/api/security/digest", map[string]any{}, nil); s != http.StatusOK {
		t.Fatalf("member security digest, all hosts: %d", s)
	}
	if len(fd.secScopes) != 2 || fd.secScopes[0].HostID != hostA.ID || fd.secScopes[1].HostID != uuid.Nil {
		t.Fatalf("security scopes: %+v", fd.secScopes)
	}

	// A host of another organisation is not found, from either endpoint.
	if s := foreign.post(logsPath, map[string]any{}, nil); s != http.StatusNotFound {
		t.Fatalf("foreign logs summary: %d", s)
	}
	if s := foreign.post("/api/security/digest", map[string]any{"hostId": hostA.ID.String()}, nil); s != http.StatusNotFound {
		t.Fatalf("foreign security digest: %d", s)
	}

	// Bad windows are rejected before the operator is called.
	for _, w := range []string{"2h", "7d", "1H", "10m"} {
		if s := member.post(logsPath, map[string]any{"window": w}, nil); s != http.StatusBadRequest {
			t.Fatalf("window %q: %d", w, s)
		}
	}
	if len(fd.logScopes) != 1 {
		t.Fatalf("bad windows reached the operator: %d calls", len(fd.logScopes))
	}
}
