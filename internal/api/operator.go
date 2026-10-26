package api

import (
	"context"
	"net/http"

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
}

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
}

// PolicyPusher tells connected agents of an organisation its new autonomy
// level at once.
type PolicyPusher interface {
	PushPolicy(orgID uuid.UUID)
}

func (a *API) registerOperator(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/operator", a.authenticate(http.HandlerFunc(a.handleGetOperatorSettings)))
	mux.Handle("PUT /api/settings/operator", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handlePutOperatorSettings))))
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
	if prev.Autonomy != next.Autonomy && a.cfg.PolicyPusher != nil {
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
