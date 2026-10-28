package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// MaxDailyTokenCap is the largest daily token cap an administrator may set.
const MaxDailyTokenCap = 50_000_000

// OperatorSettingsValue is an organisation's AI operator configuration.
type OperatorSettingsValue struct {
	AIEnabled     bool   `json:"aiEnabled"`
	Autonomy      string `json:"autonomy"` // report | suggest | act
	DailyTokenCap int64  `json:"dailyTokenCap"`
}

// OperatorProvider describes the server-wide AI provider (read-only).
type OperatorProvider struct {
	Configured bool   `json:"configured"`
	Name       string `json:"name"`
	Local      bool   `json:"local"`
	// Model is the configured model id (never the key or the base URL).
	Model string `json:"model,omitempty"`
}

// OperatorTestResult is the outcome of a provider connection test.
type OperatorTestResult struct {
	OK          bool   `json:"ok"`
	LatencyMS   int64  `json:"latencyMs"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Local       bool   `json:"local"`
	ToolCalling string `json:"toolCalling"`
	Class       string `json:"class,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Hint        string `json:"hint,omitempty"`
}

// ErrOperatorNotConfigured is returned by TestProvider when the server has no
// AI provider.
var ErrOperatorNotConfigured = errors.New("operator provider not configured")

// OperatorUsage is today's (UTC) token use of an organisation.
type OperatorUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// OperatorSettings is what the settings endpoints need from the operator
// layer; the server wires the real one, tests use a fake.
type OperatorSettings interface {
	GetSettings(ctx context.Context, orgID uuid.UUID) (OperatorSettingsValue, error)
	SetSettings(ctx context.Context, orgID uuid.UUID, v OperatorSettingsValue) error
	UsageToday(ctx context.Context, orgID uuid.UUID) (OperatorUsage, error)
	ProviderInfo() OperatorProvider
	// Available reports whether AI work can run for the org now and, if
	// not, a short reason (operator.Operator.Available).
	Available(ctx context.Context, orgID uuid.UUID) (ok bool, reason string)
	// TestProvider sends two fixed requests to the provider, whether or not
	// the org enabled the operator. A provider failure is part of the result;
	// the error is ErrOperatorNotConfigured when there is no provider.
	TestProvider(ctx context.Context, orgID uuid.UUID) (OperatorTestResult, error)
}

// PolicyPusher tells connected agents of an organisation its new autonomy
// level at once.
type PolicyPusher interface {
	PushPolicy(orgID uuid.UUID)
}

// HostDisconnecter closes a host's live agent session at once.
type HostDisconnecter interface {
	DisconnectHost(hostID uuid.UUID) bool
}

func (a *API) registerOperator(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/operator", a.authenticate(http.HandlerFunc(a.handleGetOperatorSettings)))
	mux.Handle("PUT /api/settings/operator", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handlePutOperatorSettings))))
	mux.Handle("POST /api/settings/operator/test", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleTestOperator))))
}

// operatorTestEvery is the shortest gap between two connection tests of one
// organisation.
const operatorTestEvery = 5 * time.Second

func (a *API) allowOperatorTest(org uuid.UUID) bool {
	a.opTestMu.Lock()
	defer a.opTestMu.Unlock()
	if a.opTestLast == nil {
		a.opTestLast = map[uuid.UUID]time.Time{}
	}
	now := a.now()
	for k, t := range a.opTestLast {
		if now.Sub(t) >= operatorTestEvery {
			delete(a.opTestLast, k)
		}
	}
	if _, ok := a.opTestLast[org]; ok {
		return false
	}
	a.opTestLast[org] = now
	return true
}

func (a *API) handleTestOperator(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OperatorSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "operator not configured")
		return
	}
	_, p := a.scoped(r)
	// An empty body is fine (the JSON content type is what CSRF needs); accept {}.
	var in struct{}
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	if !a.allowOperatorTest(p.OrgID) {
		writeError(w, http.StatusTooManyRequests, "connection test already running or just done; try again in a few seconds")
		return
	}
	res, err := a.cfg.OperatorSettings.TestProvider(r.Context(), p.OrgID)
	if errors.Is(err, ErrOperatorNotConfigured) {
		writeError(w, http.StatusConflict, "no AI provider is configured on this server")
		return
	}
	if err != nil {
		a.log.Error("operator connection test", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) operatorJSON(ctx context.Context, orgID uuid.UUID, v OperatorSettingsValue) (map[string]any, error) {
	ops := a.cfg.OperatorSettings
	usage, err := ops.UsageToday(ctx, orgID)
	if err != nil {
		return nil, err
	}
	ok, reason := ops.Available(ctx, orgID)
	return map[string]any{
		"aiEnabled":     v.AIEnabled,
		"autonomy":      v.Autonomy,
		"dailyTokenCap": v.DailyTokenCap,
		"provider":      ops.ProviderInfo(),
		"usageToday":    usage,
		"available":     ok,
		"reason":        reason,
	}, nil
}

func (a *API) handleGetOperatorSettings(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OperatorSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "operator not configured")
		return
	}
	_, p := a.scoped(r)
	v, err := a.cfg.OperatorSettings.GetSettings(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("get operator settings", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out, err := a.operatorJSON(r.Context(), p.OrgID, v)
	if err != nil {
		a.log.Error("operator usage", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handlePutOperatorSettings(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OperatorSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "operator not configured")
		return
	}
	_, p := a.scoped(r)
	var in struct {
		AIEnabled     *bool   `json:"aiEnabled"`
		Autonomy      *string `json:"autonomy"`
		DailyTokenCap *int64  `json:"dailyTokenCap"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.AIEnabled == nil || in.Autonomy == nil || in.DailyTokenCap == nil {
		writeError(w, http.StatusBadRequest, "aiEnabled, autonomy and dailyTokenCap are required")
		return
	}
	switch *in.Autonomy {
	case "report", "suggest", "act":
	default:
		writeError(w, http.StatusBadRequest, "autonomy must be report, suggest or act")
		return
	}
	if *in.DailyTokenCap < 0 || *in.DailyTokenCap > MaxDailyTokenCap {
		writeError(w, http.StatusBadRequest, "dailyTokenCap must be between 0 and 50000000")
		return
	}
	next := OperatorSettingsValue{AIEnabled: *in.AIEnabled, Autonomy: *in.Autonomy, DailyTokenCap: *in.DailyTokenCap}
	ops := a.cfg.OperatorSettings
	prev, err := ops.GetSettings(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("get operator settings", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	if err := ops.SetSettings(r.Context(), p.OrgID, next); err != nil {
		a.log.Error("set operator settings", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	a.log.Info("audit: operator settings changed",
		"org", p.OrgID, "user", p.UserID,
		"ai_enabled", next.AIEnabled, "autonomy", next.Autonomy, "daily_token_cap", next.DailyTokenCap,
		"prev_ai_enabled", prev.AIEnabled, "prev_autonomy", prev.Autonomy, "prev_daily_token_cap", prev.DailyTokenCap)
	if (prev.Autonomy != next.Autonomy || prev.AIEnabled != next.AIEnabled) && a.cfg.PolicyPusher != nil {
		a.cfg.PolicyPusher.PushPolicy(p.OrgID)
	}
	out, err := a.operatorJSON(r.Context(), p.OrgID, next)
	if err != nil {
		a.log.Error("operator usage", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
