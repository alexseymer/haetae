package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
)

// Digester is what the digest endpoints need from the operator. Nil answers
// 503.
type Digester interface {
	SummarizeLogs(ctx context.Context, orgID uuid.UUID, scope operator.LogScope) (operator.Digest, error)
	SecurityDigest(ctx context.Context, orgID uuid.UUID, scope operator.SecurityScope) (operator.Digest, error)
}

func (a *API) registerDigest(mux *http.ServeMux) {
	mux.Handle("POST /api/hosts/{id}/logs/summary", a.authenticate(http.HandlerFunc(a.handleLogSummary)))
	mux.Handle("POST /api/security/digest", a.authenticate(http.HandlerFunc(a.handleSecurityDigest)))
}

// digestBody reads an optional JSON object body. An empty body is a zero value.
func digestBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		return false
	}
	if strings.TrimSpace(string(raw)) == "" {
		return true
	}
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	return decodeJSON(w, r, dst)
}

// writeDigestError maps the digest errors to HTTP statuses. Other errors are
// logged and answered 503; the detail never reaches the client.
func (a *API) writeDigestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, operator.ErrScopeNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, operator.ErrBadWindow):
		writeError(w, http.StatusBadRequest, "window must be 1h, 6h or 24h")
	default:
		a.log.Error("digest", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
	}
}

// handleLogSummary: POST /api/hosts/{id}/logs/summary with an optional body
// {"window":"1h|6h|24h","containerId":"..."}. Default window 24h.
func (a *API) handleLogSummary(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Digests == nil {
		writeError(w, http.StatusServiceUnavailable, "operator not configured")
		return
	}
	st, p := a.scoped(r)
	hostID, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
		return
	}
	var in struct {
		Window      string `json:"window"`
		ContainerID string `json:"containerId"`
	}
	if !digestBody(w, r, &in) {
		return
	}
	win, err := operator.ParseDigestWindow(in.Window)
	if err != nil {
		a.writeDigestError(w, err)
		return
	}
	d, err := a.cfg.Digests.SummarizeLogs(r.Context(), p.OrgID, operator.LogScope{
		HostID: hostID, ContainerID: in.ContainerID, Window: win,
	})
	if err != nil {
		a.writeDigestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleSecurityDigest: POST /api/security/digest with an optional body
// {"hostId":"..."}. Without a host it covers every host of the organisation.
func (a *API) handleSecurityDigest(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Digests == nil {
		writeError(w, http.StatusServiceUnavailable, "operator not configured")
		return
	}
	st, p := a.scoped(r)
	var in struct {
		HostID string `json:"hostId"`
	}
	if !digestBody(w, r, &in) {
		return
	}
	var hostID uuid.UUID
	if in.HostID != "" {
		id, ok := parseHostID(w, in.HostID)
		if !ok {
			return
		}
		if _, ok := a.requireHost(w, r, st, p.OrgID, id); !ok {
			return
		}
		hostID = id
	}
	d, err := a.cfg.Digests.SecurityDigest(r.Context(), p.OrgID, operator.SecurityScope{HostID: hostID})
	if err != nil {
		a.writeDigestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
