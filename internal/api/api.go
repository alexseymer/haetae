// Package api is the control plane's JSON API for the web UI: accounts,
// sessions, and read access to hosts, metrics and logs.
//
// Every request that needs a signed-in user passes through authenticate.
// Organisation data is read and written through a Store.ForOrg view, so the
// database restricts it to the user's organisation; sign-in, sessions and
// passwords use the server's own role, because the restricted role cannot
// reach them.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/deploys"
	"github.com/alexseymer/haetae/internal/secrets"
	"github.com/alexseymer/haetae/internal/store"
)

// Session lifetimes: a session ends after sessionIdle without use, and never
// lasts longer than sessionMax.
const (
	sessionIdle  = 7 * 24 * time.Hour
	sessionMax   = 30 * 24 * time.Hour
	touchEvery   = time.Minute
	cookieName   = "haetae_session"
	maxBodyBytes = 1 << 20
)

// Config configures the API.
type Config struct {
	Store   *store.Store
	Log     *slog.Logger
	Version string

	// SetupCode must be presented to create the first account. Empty means
	// setup is disabled (the server generates and prints one at start).
	SetupCode string

	// WebURL, GatewayURL, CAPin and WebPin are shown in agent enrollment
	// commands. WebURL is where the UI and /install.sh are served, GatewayURL
	// where agents connect, CAPin the fingerprint of the CA that signs the
	// gateway certificate, WebPin the curl --pinnedpubkey value for the web
	// certificate (empty when it is a publicly trusted certificate).
	WebURL     string
	GatewayURL string
	CAPin      string
	WebPin     string

	// OnSetupDone runs once, after the first administrator was created.
	OnSetupDone func()

	// InsecureCookies drops the Secure flag, for plain-HTTP development only.
	InsecureCookies bool
	// TrustedProxies are the addresses of reverse proxies in front of the
	// server. Only a request that arrives from one of them has its
	// X-Forwarded-For honoured; from anyone else the header is ignored, so a
	// client cannot pick its own address for rate limiting.
	TrustedProxies []netip.Prefix

	// Now is the clock; default time.Now.
	Now func() time.Time

	// Deploys starts and rolls back deploys. Nil disables those endpoints.
	Deploys *deploys.Service
	// Secrets seals git tokens at rest. Required to create projects with tokens.
	Secrets *secrets.Box
	// RefreshWatch pushes a new WatchSet to a connected agent after project changes.
	RefreshWatch func(hostID uuid.UUID)

	// Chat handles inbound web messages, actions and channel enablement.
	// Nil disables chat write endpoints; channel list still reports web as enabled.
	Chat *chat.Service

	// OperatorSettings backs /api/settings/operator. Nil answers 503.
	OperatorSettings OperatorSettings
	// Digests backs the log summary and security digest endpoints. Nil answers 503.
	Digests Digester
	// PolicyPusher is told when an organisation's autonomy level changes.
	PolicyPusher PolicyPusher
	// HostDisconnecter closes the live agent session of a removed host. Nil
	// skips it (the agent is then refused at its next connect).
	HostDisconnecter HostDisconnecter

	// Sender delivers agent commands (forced container scans). When nil, the
	// Deploys service's Sender is used if set.
	Sender deploys.Sender
}

// API serves the JSON endpoints.
type API struct {
	cfg Config
	st  *store.Store
	log *slog.Logger
	now func() time.Time

	mu        sync.Mutex
	setupCode string

	loginByAccount *auth.Limiter // email + address
	loginByAddr    *auth.Limiter // address only
	loginByEmail   *auth.Limiter // email only: many addresses cannot add up to unlimited guesses

	linkConfirm *auth.Limiter // failed email link confirmations per user

	opTestMu   sync.Mutex
	opTestLast map[uuid.UUID]time.Time // org -> last connection test
	scanMu     sync.Mutex
	scanLast   map[string]time.Time // host|container → last forced scan
}

// New returns an API.
func New(cfg Config) *API {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &API{
		cfg:            cfg,
		st:             cfg.Store,
		log:            cfg.Log,
		now:            cfg.Now,
		setupCode:      cfg.SetupCode,
		loginByAccount: auth.NewLimiter(8, 15*time.Minute),
		loginByAddr:    auth.NewLimiter(40, 15*time.Minute),
		loginByEmail:   auth.NewLimiter(30, 15*time.Minute),
		linkConfirm:    auth.NewLimiter(5, store.LinkCodeTTL),
	}
}

// Principal is the signed-in user of a request.
type Principal struct {
	SessionID uuid.UUID
	UserID    uuid.UUID
	OrgID     uuid.UUID
	Email     string
	Role      string
	tokenHash []byte
}

type ctxKey struct{}

func principalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Handler returns the routes under /api/ plus /healthz.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /api/status", a.handleStatus)
	mux.HandleFunc("POST /api/setup", a.handleSetup)
	mux.HandleFunc("POST /api/login", a.handleLogin)

	// Signed-in.
	mux.Handle("POST /api/logout", a.authenticate(http.HandlerFunc(a.handleLogout)))
	mux.Handle("GET /api/me", a.authenticate(http.HandlerFunc(a.handleMe)))
	mux.Handle("POST /api/password", a.authenticate(http.HandlerFunc(a.handlePassword)))
	mux.Handle("GET /api/users", a.authenticate(http.HandlerFunc(a.handleListUsers)))
	mux.Handle("POST /api/users", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleCreateUser))))
	a.registerData(mux)
	a.registerProjects(mux)
	a.registerAlerts(mux)
	a.registerChat(mux)
	a.registerSecurity(mux)
	a.registerOperator(mux)
	a.registerDigest(mux)

	// Anything else under /api is a 404 in JSON, never the web app.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return a.base(mux)
}

// base applies the rules every request must meet.
func (a *API) base(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		if !a.cfg.InsecureCookies {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

		// State-changing requests must be JSON with a custom header. A
		// cross-site form or fetch cannot set either without a CORS
		// preflight, which this server never allows; with SameSite=Strict
		// cookies this closes cross-site request forgery.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get("X-Haetae-Request") != "1" {
				writeError(w, http.StatusForbidden, "missing X-Haetae-Request header")
				return
			}
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate requires a live session and puts the Principal in the context.
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || c.Value == "" || len(c.Value) > 128 {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		hash := auth.HashToken(c.Value)
		now := a.now()
		s, err := a.st.SessionByTokenHash(r.Context(), hash, now)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				a.log.Error("load session", "err", err)
				writeError(w, http.StatusServiceUnavailable, "try again")
				return
			}
			a.clearCookie(w)
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if now.Sub(s.LastSeenAt) > touchEvery {
			exp := minTime(s.CreatedAt.Add(sessionMax), now.Add(sessionIdle))
			if err := a.st.TouchSession(r.Context(), s.ID, now, exp); err != nil {
				a.log.Warn("touch session", "err", err)
			} else {
				// Move the browser's cookie along with the server-side expiry,
				// or an active user would be signed out when the first cookie
				// lapses.
				a.setCookie(w, c.Value, exp)
			}
		}
		p := Principal{SessionID: s.ID, UserID: s.UserID, OrgID: s.OrgID, Email: s.Email, Role: s.Role, tokenHash: hash}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func (a *API) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := principalFrom(r.Context()); p.Role != "admin" {
			writeError(w, http.StatusForbidden, "administrator only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// scoped returns the store restricted to the request's organisation.
func (a *API) scoped(r *http.Request) (*store.Store, Principal) {
	p, _ := principalFrom(r.Context())
	return a.st.ForOrg(p.OrgID), p
}

func (a *API) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is on unless InsecureCookies (plain-HTTP development) is set
		Name: cookieName, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: !a.cfg.InsecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (a *API) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: same as setCookie
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !a.cfg.InsecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

// clientAddr is the address used for rate limiting. X-Forwarded-For is used
// only when the request comes from a trusted proxy, and then the right-most
// entry that is not itself a trusted proxy is taken: the proxies append the
// address they saw, so anything a client wrote earlier in the list is ignored.
func (a *API) clientAddr(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if len(a.cfg.TrustedProxies) == 0 || !a.trusted(peer) {
		return peer
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := strings.TrimSpace(parts[i])
		if ip == "" {
			continue
		}
		if _, err := netip.ParseAddr(ip); err != nil {
			return peer // garbage from the proxy chain: do not trust the header at all
		}
		if !a.trusted(ip) {
			return ip
		}
	}
	return peer
}

func (a *API) trusted(addr string) bool {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, p := range a.cfg.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// constantTimeEqual compares secrets without leaking where they differ.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// decodeJSON reads one JSON object, rejecting unknown fields and trailing data.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON")
		}
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
