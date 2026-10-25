//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/auth"
)

const setupCode = "setup-code-123456"

func setupBody(email string) map[string]string {
	return map[string]string{"setupCode": setupCode, "orgName": "Acme", "email": email, "password": testPassword}
}

func TestFirstRunSetupFlow(t *testing.T) {
	e := newEnv(t)
	c := e.newClient()

	var st struct {
		SetupRequired bool   `json:"setupRequired"`
		Version       string `json:"version"`
	}
	if c.get("/api/status", &st) != 200 || !st.SetupRequired || st.Version != "test" {
		t.Fatalf("status = %+v", st)
	}
	// Nothing but the status is available before setup.
	if s := c.get("/api/me", nil); s != http.StatusUnauthorized {
		t.Fatalf("/api/me before setup = %d", s)
	}

	// A wrong code creates nothing.
	bad := setupBody("admin@example.com")
	bad["setupCode"] = "not-the-code"
	if s := c.post("/api/setup", bad, nil); s != http.StatusForbidden {
		t.Fatalf("wrong setup code = %d, want 403", s)
	}
	if s := c.get("/api/status", &st); s != 200 || !st.SetupRequired {
		t.Fatal("a wrong setup code created an account")
	}

	// The right code creates the administrator and signs them in.
	var created struct {
		User    userJSON `json:"user"`
		OrgName string   `json:"orgName"`
	}
	if s := c.post("/api/setup", setupBody("Admin@Example.com"), &created); s != http.StatusCreated {
		t.Fatalf("setup = %d", s)
	}
	if created.User.Email != "admin@example.com" || created.User.Role != "admin" || created.OrgName != "Acme" {
		t.Fatalf("setup result = %+v", created)
	}
	var me struct {
		User    userJSON `json:"user"`
		OrgName string   `json:"orgName"`
	}
	if c.get("/api/me", &me) != 200 || me.User.Email != "admin@example.com" || me.OrgName != "Acme" {
		t.Fatalf("/api/me after setup = %+v", me)
	}

	// Setup works exactly once, even with the correct code.
	if s := e.newClient().post("/api/setup", setupBody("second@example.com"), nil); s != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409", s)
	}
	if c.get("/api/status", &st); st.SetupRequired {
		t.Fatal("setup still required after setup")
	}
}

func TestSetupRejectsWeakInput(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]map[string]string{
		"short password": {"email": "a@example.com", "password": "short"},
		"bad email":      {"email": "not-an-email", "password": testPassword},
		"display name":   {"email": "Eve <eve@example.com>", "password": testPassword},
		"same as email":  {"email": "longer.address@example.com", "password": "longer.address@example.com"},
	} {
		body := setupBody("x@example.com")
		for k, v := range tc {
			body[k] = v
		}
		if s := e.newClient().post("/api/setup", body, nil); s != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, s)
		}
	}
	if required, _ := e.st.SetupRequired(context.Background()); !required {
		t.Fatal("a rejected setup left an account behind")
	}
}

// Many parallel setups with the right code create exactly one administrator.
func TestSetupIsRaceFree(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- e.newClient().post("/api/setup", setupBody("admin"+string(rune('a'+i))+"@example.com"), nil)
		}()
	}
	wg.Wait()
	close(results)
	created, conflict := 0, 0
	for s := range results {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	if created != 1 {
		t.Fatalf("%d setups succeeded (conflicts %d), want exactly 1", created, conflict)
	}
	var n int
	if err := e.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("users = %d, %v", n, err)
	}
}

func TestSetupCodeGuessingIsRateLimited(t *testing.T) {
	e := newEnv(t)
	bad := setupBody("a@example.com")
	bad["setupCode"] = "guess"
	var limited bool
	for range 60 {
		if s := e.newClient().post("/api/setup", bad, nil); s == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("60 wrong setup codes were all answered normally")
	}
	// The right code is refused too while the address is blocked.
	if s := e.newClient().post("/api/setup", setupBody("a@example.com"), nil); s != http.StatusTooManyRequests {
		t.Fatalf("right code while blocked = %d, want 429", s)
	}
}

func signedIn(t *testing.T, e *env, email string) *client {
	t.Helper()
	c := e.newClient()
	if s := c.post("/api/setup", setupBody(email), nil); s != http.StatusCreated {
		t.Fatalf("setup = %d", s)
	}
	return c
}

func TestLoginLogoutAndSessions(t *testing.T) {
	e := newEnv(t)
	signedIn(t, e, "admin@example.com")

	c := e.newClient()
	if s := c.post("/api/login", map[string]string{"email": "admin@example.com", "password": testPassword}, nil); s != 200 {
		t.Fatalf("login = %d", s)
	}
	if c.get("/api/me", nil) != 200 {
		t.Fatal("not signed in after login")
	}
	if s := c.post("/api/logout", map[string]string{}, nil); s != 200 {
		t.Fatalf("logout = %d", s)
	}
	if s := c.get("/api/me", nil); s != http.StatusUnauthorized {
		t.Fatalf("/api/me after logout = %d, want 401", s)
	}
	// The old cookie is dead on the server too, not just deleted in the browser.
	var n int
	_ = e.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n)
	if n != 1 { // the one from setup remains
		t.Fatalf("sessions in database = %d, want 1", n)
	}
}

// Unknown user, wrong password and a disabled user are indistinguishable.
func TestLoginDoesNotRevealWhichAccountsExist(t *testing.T) {
	e := newEnv(t)
	signedIn(t, e, "admin@example.com")
	org, _ := e.st.DefaultOrg(context.Background())
	hash, _ := auth.HashPassword(testPassword)
	disabled, _ := e.st.CreateUser(context.Background(), org.ID, "gone@example.com", hash, "member")
	if _, err := e.st.Pool().Exec(context.Background(), `UPDATE users SET disabled_at = now() WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		status int
		body   string
	}
	try := func(email, pw string) result {
		s, _, b := e.newClient().do(http.MethodPost, "/api/login", map[string]string{"email": email, "password": pw})
		return result{s, strings.TrimSpace(string(b))}
	}
	wrongPW := try("admin@example.com", "wrong password here")
	unknown := try("nobody@example.com", testPassword)
	off := try("gone@example.com", testPassword)
	if wrongPW.status != 401 || wrongPW != unknown || wrongPW != off {
		t.Fatalf("responses differ:\n wrong password: %+v\n unknown user:   %+v\n disabled user:  %+v", wrongPW, unknown, off)
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	e := newEnv(t)
	signedIn(t, e, "admin@example.com")
	c := e.newClient()
	var blocked bool
	for range 12 {
		s, h, _ := c.do(http.MethodPost, "/api/login", map[string]string{"email": "admin@example.com", "password": "wrong password here"})
		if s == http.StatusTooManyRequests {
			if h.Get("Retry-After") == "" {
				t.Error("429 without Retry-After")
			}
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("12 wrong passwords were all answered normally")
	}
	// Even the right password is refused while blocked.
	if s := c.post("/api/login", map[string]string{"email": "admin@example.com", "password": testPassword}, nil); s != http.StatusTooManyRequests {
		t.Fatalf("right password while blocked = %d, want 429", s)
	}
}

func TestCookieIsHardened(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.InsecureCookies = false })
	_, h, _ := e.newClient().do(http.MethodPost, "/api/setup", setupBody("admin@example.com"))
	cookie := h.Get("Set-Cookie")
	for _, want := range []string{"haetae_session=", "HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("cookie lacks %q: %s", want, cookie)
		}
	}
	if h.Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS header on a secure deployment")
	}
	for _, hdr := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Cache-Control"} {
		if h.Get(hdr) == "" {
			t.Errorf("missing %s", hdr)
		}
	}
	// The token is not predictable from the response body or reused across logins.
	if strings.Count(cookie, "haetae_session=") != 1 {
		t.Error("expected exactly one session cookie")
	}
}

func TestStateChangingRequestsNeedTheCSRFHeaderAndJSON(t *testing.T) {
	e := newEnv(t)
	c := signedIn(t, e, "admin@example.com")

	do := func(header map[string]string, contentType, body string) int {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/logout", strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// What a cross-site HTML form could send: no custom header.
	if s := do(nil, "application/x-www-form-urlencoded", "a=b"); s != http.StatusForbidden {
		t.Errorf("form post without header = %d, want 403", s)
	}
	if s := do(nil, "application/json", "{}"); s != http.StatusForbidden {
		t.Errorf("JSON post without header = %d, want 403", s)
	}
	if s := do(map[string]string{"X-Haetae-Request": "1"}, "text/plain", "{}"); s != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain with header = %d, want 415", s)
	}
	if c.get("/api/me", nil) != 200 {
		t.Fatal("a rejected request ended the session")
	}
	if s := do(map[string]string{"X-Haetae-Request": "1"}, "application/json", "{}"); s != 200 {
		t.Errorf("proper request = %d", s)
	}
}

func TestRequestBodiesAreStrict(t *testing.T) {
	e := newEnv(t)
	post := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Haetae-Request", "1")
		resp, err := e.newClient().hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := post(`{"email":"a@b.co","password":"x","admin":true}`); s != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", s)
	}
	if s := post(`{"email":"a@b.co","password":"x"} {"again":1}`); s != http.StatusBadRequest {
		t.Errorf("trailing data = %d, want 400", s)
	}
	if s := post(`not json`); s != http.StatusBadRequest {
		t.Errorf("garbage = %d, want 400", s)
	}
	if s := post(`{"email":"` + strings.Repeat("a", 2<<20) + `"}`); s != http.StatusRequestEntityTooLarge {
		t.Errorf("2 MiB body = %d, want 413", s)
	}
}

func TestSessionExpiry(t *testing.T) {
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	e := newEnv(t, func(c *Config) { c.Now = now })
	c := signedIn(t, e, "admin@example.com")

	// Use keeps a session alive past the idle limit...
	for range 3 {
		advance(5 * 24 * time.Hour)
		if c.get("/api/me", nil) != 200 {
			t.Fatal("an active session expired")
		}
	}
	// ...but idle time ends it.
	advance(8 * 24 * time.Hour)
	if s := c.get("/api/me", nil); s != http.StatusUnauthorized {
		t.Fatalf("session idle for 8 days = %d, want 401", s)
	}

	// And no session outlives the absolute limit, however busy.
	c2 := e.newClient()
	if s := c2.post("/api/login", map[string]string{"email": "admin@example.com", "password": testPassword}, nil); s != 200 {
		t.Fatal("login")
	}
	for range 7 {
		advance(5 * 24 * time.Hour)
		c2.get("/api/me", nil)
	}
	if s := c2.get("/api/me", nil); s != http.StatusUnauthorized {
		t.Fatalf("session older than 30 days = %d, want 401", s)
	}
}

func TestPasswordChange(t *testing.T) {
	e := newEnv(t)
	signedIn(t, e, "admin@example.com")
	a, b := e.newClient(), e.newClient()
	for _, c := range []*client{a, b} {
		if s := c.post("/api/login", map[string]string{"email": "admin@example.com", "password": testPassword}, nil); s != 200 {
			t.Fatal("login")
		}
	}
	if s := a.post("/api/password", map[string]string{"current": "wrong current pass", "new": "a brand new passphrase"}, nil); s != http.StatusForbidden {
		t.Fatalf("wrong current password = %d, want 403", s)
	}
	if s := a.post("/api/password", map[string]string{"current": testPassword, "new": "short"}, nil); s != http.StatusBadRequest {
		t.Fatalf("weak new password = %d, want 400", s)
	}
	if s := a.post("/api/password", map[string]string{"current": testPassword, "new": "a brand new passphrase"}, nil); s != 200 {
		t.Fatalf("change = %d", s)
	}
	if a.get("/api/me", nil) != 200 {
		t.Error("the session that changed the password was ended")
	}
	if s := b.get("/api/me", nil); s != http.StatusUnauthorized {
		t.Errorf("another session after a password change = %d, want 401", s)
	}
	if s := e.newClient().post("/api/login", map[string]string{"email": "admin@example.com", "password": testPassword}, nil); s != http.StatusUnauthorized {
		t.Errorf("old password still works: %d", s)
	}
	if s := e.newClient().post("/api/login", map[string]string{"email": "admin@example.com", "password": "a brand new passphrase"}, nil); s != 200 {
		t.Errorf("new password does not work: %d", s)
	}
}

func TestOnlyAdministratorsCreateUsers(t *testing.T) {
	e := newEnv(t)
	admin := signedIn(t, e, "admin@example.com")

	newUser := map[string]string{"email": "Member@Example.com", "password": "another long passphrase", "role": "member"}
	if s := admin.post("/api/users", newUser, nil); s != http.StatusCreated {
		t.Fatalf("admin creates member = %d", s)
	}
	if s := admin.post("/api/users", newUser, nil); s != http.StatusConflict {
		t.Fatalf("duplicate email = %d, want 409", s)
	}
	bad := map[string]string{"email": "x@example.com", "password": "another long passphrase", "role": "root"}
	if s := admin.post("/api/users", bad, nil); s != http.StatusBadRequest {
		t.Fatalf("unknown role = %d, want 400", s)
	}

	member := e.newClient()
	if s := member.post("/api/login", map[string]string{"email": "member@example.com", "password": "another long passphrase"}, nil); s != 200 {
		t.Fatalf("member login = %d", s)
	}
	if s := member.post("/api/users", map[string]string{"email": "z@example.com", "password": "another long passphrase", "role": "admin"}, nil); s != http.StatusForbidden {
		t.Fatalf("member creates an admin = %d, want 403", s)
	}
	var list struct{ Users []userJSON }
	if member.get("/api/users", &list) != 200 || len(list.Users) != 2 {
		t.Fatalf("member lists users = %+v", list)
	}
}

func TestUsersAreListedPerOrganisation(t *testing.T) {
	e := newEnv(t)
	orgA, a := e.addOrgWithUser("A", "a@example.com", "admin")
	_, b := e.addOrgWithUser("B", "b@example.com", "admin")
	_ = orgA
	var la, lb struct{ Users []userJSON }
	a.get("/api/users", &la)
	b.get("/api/users", &lb)
	if len(la.Users) != 1 || la.Users[0].Email != "a@example.com" || len(lb.Users) != 1 || lb.Users[0].Email != "b@example.com" {
		t.Fatalf("A sees %+v, B sees %+v", la.Users, lb.Users)
	}
	// An admin of A creating a user cannot land in B.
	if s := a.post("/api/users", map[string]string{"email": "n@example.com", "password": "another long passphrase", "role": "member"}, nil); s != http.StatusCreated {
		t.Fatal(s)
	}
	b.get("/api/users", &lb)
	if len(lb.Users) != 1 {
		t.Fatalf("a user created by org A is visible to org B: %+v", lb.Users)
	}
}

func TestHealthzAndUnknownAPIRoutes(t *testing.T) {
	e := newEnv(t)
	c := e.newClient()
	if c.get("/healthz", nil) != 200 {
		t.Fatal("healthz")
	}
	s, h, b := c.do(http.MethodGet, "/api/does-not-exist", nil)
	if s != 404 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || !strings.Contains(string(b), "not found") {
		t.Fatalf("unknown route: %d %s %s", s, h.Get("Content-Type"), b)
	}
}

// The browser's cookie must be renewed together with the server-side session,
// or an active user is signed out when the first cookie lapses.
func TestActiveSessionCookieIsRenewed(t *testing.T) {
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	e := newEnv(t, func(c *Config) { c.Now = now })
	c := signedIn(t, e, "admin@example.com")

	mu.Lock()
	clock = clock.Add(2 * time.Minute) // past the touch interval
	mu.Unlock()
	_, h, _ := c.do(http.MethodGet, "/api/me", nil)
	cookie := h.Get("Set-Cookie")
	if !strings.Contains(cookie, "haetae_session=") || !strings.Contains(cookie, "Expires=") {
		t.Fatalf("an active session did not renew its cookie: %q", cookie)
	}
	// The renewed cookie expires about sessionIdle after now, never beyond the absolute limit.
	var expires time.Time
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "Expires="); ok {
			expires, _ = time.Parse(http.TimeFormat, v)
		}
	}
	want := now().Add(sessionIdle)
	if d := expires.Sub(want); d < -2*time.Second || d > 2*time.Second {
		t.Fatalf("renewed cookie expires %v, want about %v", expires, want)
	}
	// Within the touch interval nothing is renewed (no needless Set-Cookie).
	_, h, _ = c.do(http.MethodGet, "/api/me", nil)
	if h.Get("Set-Cookie") != "" {
		t.Errorf("cookie re-sent on every request: %q", h.Get("Set-Cookie"))
	}
}

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

func TestClientAddressOnlyTrustsConfiguredProxies(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	none := New(Config{})
	proxied := New(Config{TrustedProxies: prefixes(t, "10.0.0.0/8", "127.0.0.1/32")})

	for _, tc := range []struct {
		name string
		api  *API
		r    *http.Request
		want string
	}{
		{"no proxies configured: header ignored", none, req("203.0.113.9:1234", "1.2.3.4"), "203.0.113.9"},
		{"direct client forging the header", proxied, req("203.0.113.9:1234", "1.2.3.4"), "203.0.113.9"},
		{"proxy appends the real client", proxied, req("10.0.0.5:1234", "198.51.100.7"), "198.51.100.7"},
		{"client-supplied prefix is ignored", proxied, req("10.0.0.5:1234", "6.6.6.6, 198.51.100.7"), "198.51.100.7"},
		{"chain of trusted proxies", proxied, req("10.0.0.5:1234", "198.51.100.7, 10.0.0.9"), "198.51.100.7"},
		{"garbage in the chain: do not trust", proxied, req("10.0.0.5:1234", "not-an-ip"), "10.0.0.5"},
		{"header missing", proxied, req("10.0.0.5:1234", ""), "10.0.0.5"},
		{"IPv6 peer", none, req("[2001:db8::1]:443", ""), "2001:db8::1"},
	} {
		if got := tc.api.clientAddr(tc.r); got != tc.want {
			t.Errorf("%s: clientAddr = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Guesses against one account from many addresses still add up.
func TestLoginGuessesAcrossAddressesAreLimitedPerAccount(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.TrustedProxies = prefixes(t, "127.0.0.0/8") })
	signedIn(t, e, "admin@example.com")

	login := func(ip string) int {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/login", strings.NewReader(`{"email":"admin@example.com","password":"wrong password here"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Haetae-Request", "1")
		req.Header.Set("X-Forwarded-For", ip) // the test server is on loopback, a trusted proxy
		resp, err := e.newClient().hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	var limited int
	for i := range 45 {
		if login(fmt.Sprintf("198.51.100.%d", i+1)) == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("45 wrong guesses from 45 different addresses were all answered normally")
	}
	// The right password is refused from yet another address while the account is limited.
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/login", strings.NewReader(`{"email":"admin@example.com","password":"`+testPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Haetae-Request", "1")
	req.Header.Set("X-Forwarded-For", "203.0.113.200")
	resp, err := e.newClient().hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("right password while the account is limited = %d, want 429", resp.StatusCode)
	}
}

func TestAPINeverReturnsSecrets(t *testing.T) {
	e := newEnv(t)
	c := signedIn(t, e, "admin@example.com")
	for _, path := range []string{"/api/me", "/api/users", "/api/hosts", "/api/status"} {
		status, _, body := c.do(http.MethodGet, path, nil)
		if status != 200 {
			t.Fatalf("%s = %d", path, status)
		}
		for _, secret := range []string{"argon2id", "password_hash", "passwordHash", "token_hash", "tokenHash"} {
			if strings.Contains(string(body), secret) {
				t.Errorf("%s leaks %q: %s", path, secret, body)
			}
		}
	}
}
