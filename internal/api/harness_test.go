//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestMain(m *testing.M) {
	auth.UseCheapHashingInTests()
	os.Exit(m.Run())
}

const testPassword = "a long test passphrase"

// env is an API on a fresh database behind an httptest server.
type env struct {
	t   *testing.T
	st  *store.Store
	api *API
	ts  *httptest.Server
}

// newEnv starts an API with setup code "setup-code-123456" and plain-HTTP
// cookies (httptest serves HTTP; Secure cookies would never come back).
func newEnv(t *testing.T, tweak ...func(*Config)) *env {
	t.Helper()
	st := storetest.New(t)
	if err := st.EnsurePartitions(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Store: st, SetupCode: "setup-code-123456", InsecureCookies: true, Version: "test",
		WebURL: "https://web.test:8080", GatewayURL: "https://gw.test:8443", CAPin: "sha256:testpin", WebPin: "sha256//testwebpin",
	}
	for _, f := range tweak {
		f(&cfg)
	}
	e := &env{t: t, st: st, api: New(cfg)}
	e.ts = httptest.NewServer(e.api.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

// client is an HTTP client with its own cookie jar (its own browser).
type client struct {
	e  *env
	hc *http.Client
}

func (e *env) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, hc: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// do sends a request the way the web app does: JSON with the CSRF header.
func (c *client) do(method, path string, body any) (int, http.Header, []byte) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.e.ts.URL+path, rd)
	if err != nil {
		c.e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-Haetae-Request", "1")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, out
}

// get decodes a JSON response into out (if non-nil) and returns the status.
func (c *client) get(path string, out any) int {
	c.e.t.Helper()
	status, _, b := c.do(http.MethodGet, path, nil)
	if out != nil && status == http.StatusOK {
		if err := json.Unmarshal(b, out); err != nil {
			c.e.t.Fatalf("GET %s: %v\n%s", path, err, b)
		}
	}
	return status
}

// post sends JSON and decodes a JSON response into out (if non-nil).
func (c *client) post(path string, body, out any) int {
	c.e.t.Helper()
	status, _, b := c.do(http.MethodPost, path, body)
	if out != nil && status < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			c.e.t.Fatalf("POST %s: %v\n%s", path, err, b)
		}
	}
	return status
}

// addOrgWithUser creates an organisation, a user in it and returns a client
// signed in as that user. Organisation data is created through e.st.
func (e *env) addOrgWithUser(orgName, email, role string) (store.Org, *client) {
	e.t.Helper()
	ctx := context.Background()
	org, err := e.st.CreateOrg(ctx, orgName)
	if err != nil {
		e.t.Fatal(err)
	}
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.CreateUser(ctx, org.ID, email, hash, role); err != nil {
		e.t.Fatal(err)
	}
	c := e.newClient()
	if s := c.post("/api/login", map[string]string{"email": email, "password": testPassword}, nil); s != http.StatusOK {
		e.t.Fatalf("login as %s: %d", email, s)
	}
	return org, c
}

// addHost enrolls a host in an organisation and returns it.
func (e *env) addHost(orgID uuid.UUID, name string) store.Host {
	e.t.Helper()
	ctx := context.Background()
	tok, _, err := e.st.CreateEnrollmentToken(ctx, orgID, name, time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	h, err := e.st.EnrollHost(ctx, tok, store.HostInfo{Hostname: name + ".example", OS: "Debian", Kernel: "6.1", Arch: "amd64", DockerVersion: "27.0", AgentVersion: "test"},
		func(uuid.UUID) (string, error) { return "serial-" + name, nil })
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}
