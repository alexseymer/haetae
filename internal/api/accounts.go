package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/store"
)

type userJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()
	if err := a.st.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStatus tells the web app whether to show setup or sign-in.
func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	required, err := a.st.SetupRequired(r.Context())
	if err != nil {
		a.log.Error("setup required", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setupRequired": required, "version": a.cfg.Version})
}

// validEmail accepts a plain address of reasonable length and returns it
// lower-cased.
func validEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || addr.Name != "" {
		return "", false
	}
	return strings.ToLower(addr.Address), true
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request, u store.User) bool {
	token, hash, err := auth.NewToken()
	if err != nil {
		a.log.Error("new token", "err", err)
		writeError(w, http.StatusInternalServerError, "try again")
		return false
	}
	now := a.now()
	expires := now.Add(sessionIdle)
	if err := a.st.CreateSession(r.Context(), u.ID, u.OrgID, hash, expires); err != nil {
		a.log.Error("create session", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return false
	}
	a.setCookie(w, token, expires)
	return true
}

func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SetupCode string `json:"setupCode"`
		OrgName   string `json:"orgName"`
		Email     string `json:"email"`
		Password  string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	addr := a.clientAddr(r)
	if ok, retry := a.loginByAddr.Allowed("setup:" + addr); !ok {
		tooManyAttempts(w, retry)
		return
	}

	a.mu.Lock()
	code := a.setupCode
	a.mu.Unlock()
	required, err := a.st.SetupRequired(r.Context())
	if err != nil {
		a.log.Error("setup required", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	if !required {
		writeError(w, http.StatusConflict, "already set up")
		return
	}
	if code == "" || !constantTimeEqual(strings.TrimSpace(in.SetupCode), code) {
		a.loginByAddr.Fail("setup:" + addr)
		writeError(w, http.StatusForbidden, "wrong setup code")
		return
	}
	email, ok := validEmail(in.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "enter a valid email address")
		return
	}
	if err := auth.CheckPassword(in.Password, email); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.OrgName) > 100 {
		writeError(w, http.StatusBadRequest, "organisation name is too long")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		a.log.Error("hash password", "err", err)
		writeError(w, http.StatusInternalServerError, "try again")
		return
	}
	u, org, err := a.st.SetupFirstUser(r.Context(), in.OrgName, email, hash)
	if errors.Is(err, store.ErrAlreadySetUp) {
		writeError(w, http.StatusConflict, "already set up")
		return
	}
	if err != nil {
		a.log.Error("setup", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	a.mu.Lock()
	a.setupCode = "" // single use
	a.mu.Unlock()
	a.log.Info("first administrator created", "email", u.Email, "org", org.Name)
	if a.cfg.OnSetupDone != nil {
		a.cfg.OnSetupDone()
	}
	if !a.startSession(w, r, u) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": userJSON{u.ID.String(), u.Email, u.Role}, "orgName": org.Name})
}

func tooManyAttempts(w http.ResponseWriter, retry time.Duration) {
	secs := int(retry.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many attempts; try again later")
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if len(in.Password) > auth.MaxPasswordLen || len(email) > 254 {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	addr := a.clientAddr(r)
	acct := email + "|" + addr
	if ok, retry := a.loginByAddr.Allowed(addr); !ok {
		tooManyAttempts(w, retry)
		return
	}
	if ok, retry := a.loginByAccount.Allowed(acct); !ok {
		tooManyAttempts(w, retry)
		return
	}
	// Guesses against one account add up across all addresses. A determined
	// attacker can therefore make an account's sign-in unavailable for a while;
	// that is the price of not allowing unlimited guessing from many addresses.
	if ok, retry := a.loginByEmail.Allowed(email); !ok {
		tooManyAttempts(w, retry)
		return
	}

	// Always do the same work, whether or not the user exists, so response
	// time does not reveal which addresses have accounts.
	u, err := a.st.UserByEmail(r.Context(), email)
	hash := auth.DummyHash()
	known := err == nil && u.DisabledAt == nil
	if known {
		hash = u.PasswordHash
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.log.Error("load user", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	match, verr := auth.VerifyPassword(in.Password, hash)
	if verr != nil {
		a.log.Error("verify password", "err", verr)
	}
	if !known || !match {
		a.loginByAccount.Fail(acct)
		a.loginByAddr.Fail(addr)
		a.loginByEmail.Fail(email)
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	a.loginByAccount.Reset(acct)
	a.loginByEmail.Reset(email)
	if !a.startSession(w, r, u) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userJSON{u.ID.String(), u.Email, u.Role}})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	if err := a.st.DeleteSession(r.Context(), p.tokenHash); err != nil {
		a.log.Error("delete session", "err", err)
	}
	a.clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	org, err := st.GetOrg(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("get org", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":    userJSON{p.UserID.String(), p.Email, p.Role},
		"orgName": org.Name,
	})
}

func (a *API) handlePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	_, p := a.scoped(r)
	if ok, retry := a.loginByAccount.Allowed("pw|" + p.UserID.String()); !ok {
		tooManyAttempts(w, retry)
		return
	}
	// The password hash is not readable by the restricted role, so this uses
	// the server's own store, and checks that it found the signed-in user.
	u, err := a.st.UserByEmail(r.Context(), p.Email)
	if err != nil || u.ID != p.UserID || u.OrgID != p.OrgID {
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	match, _ := auth.VerifyPassword(in.Current, u.PasswordHash)
	if !match {
		a.loginByAccount.Fail("pw|" + p.UserID.String())
		writeError(w, http.StatusForbidden, "current password is wrong")
		return
	}
	if err := auth.CheckPassword(in.New, p.Email); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "try again")
		return
	}
	if err := a.st.SetPassword(r.Context(), p.OrgID, p.UserID, hash); err != nil {
		a.log.Error("set password", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	a.loginByAccount.Reset("pw|" + p.UserID.String())
	// A password change ends every other session of the user.
	if err := a.st.DeleteUserSessions(r.Context(), p.UserID, p.tokenHash); err != nil {
		a.log.Error("end other sessions", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	users, err := st.ListUsers(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("list users", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, userJSON{u.ID.String(), u.Email, u.Role})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	email, ok := validEmail(in.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "enter a valid email address")
		return
	}
	if in.Role != "admin" && in.Role != "member" {
		writeError(w, http.StatusBadRequest, "role must be admin or member")
		return
	}
	if err := auth.CheckPassword(in.Password, email); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "try again")
		return
	}
	st, p := a.scoped(r)
	u, err := st.CreateUser(r.Context(), p.OrgID, email, hash, in.Role)
	if errors.Is(err, store.ErrEmailTaken) {
		writeError(w, http.StatusConflict, "that email already has an account")
		return
	}
	if err != nil {
		a.log.Error("create user", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": userJSON{u.ID.String(), u.Email, u.Role}})
}
