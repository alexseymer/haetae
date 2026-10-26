package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
)

func TestHTTPSOnlyURLsViaStore(t *testing.T) {
	in := []string{
		"https://nvd.nist.gov/vuln/detail/CVE-1",
		"http://evil.example/cve",
		"javascript:alert(1)",
		"ftp://files.example/x",
		"https://",
		"not-a-url",
	}
	got := store.HTTPSOnlyURLs(in)
	if len(got) != 1 || !strings.HasPrefix(got[0], "https://nvd.nist.gov/") {
		t.Fatalf("got %v", got)
	}
}

func TestMisconfigAndUpdateLabels(t *testing.T) {
	if store.MisconfigKindLabel("docker_socket") != "Docker socket mounted" {
		t.Fatal(store.MisconfigKindLabel("docker_socket"))
	}
	if store.ImageUpdateDecisionLabel("eligible_critical_fix") != "eligible because it fixes a critical CVE" {
		t.Fatal(store.ImageUpdateDecisionLabel("eligible_critical_fix"))
	}
	if store.ImageUpdateDecisionLabel("wait") != "waiting 48 h" {
		t.Fatal(store.ImageUpdateDecisionLabel("wait"))
	}
	if store.ImageUpdateDecisionLabel("current") != "up to date" {
		t.Fatal(store.ImageUpdateDecisionLabel("current"))
	}
}

func TestScanRateLimit(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := New(Config{Now: func() time.Time { return now }})
	if !a.allowScan("h|c") {
		t.Fatal("first should allow")
	}
	if a.allowScan("h|c") {
		t.Fatal("second within minute should deny")
	}
	now = now.Add(time.Minute + time.Second)
	a.now = func() time.Time { return now }
	if !a.allowScan("h|c") {
		t.Fatal("after a minute should allow")
	}
	a.clearScan("h|c")
	if !a.allowScan("h|c") {
		t.Fatal("after clear should allow")
	}
}

func TestScanRateMapEvictsExpiredEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := New(Config{Now: func() time.Time { return now }})
	for i := 0; i < 100; i++ {
		a.allowScan("h|c" + string(rune('a'+i%26)) + time.Duration(i).String())
	}
	now = now.Add(2 * time.Minute)
	a.now = func() time.Time { return now }
	a.allowScan("fresh")
	a.scanMu.Lock()
	n := len(a.scanLast)
	a.scanMu.Unlock()
	if n != 1 {
		t.Fatalf("scanLast has %d entries, want 1 after the rate window", n)
	}
}

func TestForceScanRequiresCSRFHeader(t *testing.T) {
	a := New(Config{})
	mux := a.Handler()
	req := httptest.NewRequest(http.MethodPost, "/api/hosts/"+strings.Repeat("0", 8)+"-0000-0000-0000-000000000001/containers/c1/scan", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	// no X-Haetae-Request
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "X-Haetae-Request") {
		t.Fatalf("body %s", rr.Body.String())
	}
}
