package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

var hostNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

var hostMetricsAllow = map[string]struct{}{
	"cpu_pct": {}, "load1": {}, "mem_used": {}, "mem_total": {},
	"disk_used": {}, "disk_total": {}, "net_rx_bps": {}, "net_tx_bps": {},
}

var containerMetricsAllow = map[string]struct{}{
	"cpu_pct": {}, "mem_usage": {}, "mem_limit": {}, "net_rx_bps": {},
	"net_tx_bps": {}, "blk_read_bps": {}, "blk_write_bps": {}, "pids": {},
}

var levelNames = []string{"unknown", "trace", "debug", "info", "warn", "error", "fatal"}

// registerData adds the read-only data endpoints (hosts, containers, metrics,
// logs, enrollment tokens).
func (a *API) registerData(mux *http.ServeMux) {
	mux.Handle("GET /api/hosts", a.authenticate(http.HandlerFunc(a.handleHosts)))
	mux.Handle("GET /api/hosts/{id}/containers/{cid}/metrics", a.authenticate(http.HandlerFunc(a.handleContainerMetrics)))
	mux.Handle("GET /api/hosts/{id}/containers", a.authenticate(http.HandlerFunc(a.handleContainers)))
	mux.Handle("GET /api/hosts/{id}/metrics", a.authenticate(http.HandlerFunc(a.handleHostMetrics)))
	mux.Handle("GET /api/hosts/{id}/logs", a.authenticate(http.HandlerFunc(a.handleHostLogs)))
	mux.Handle("GET /api/hosts/{id}", a.authenticate(http.HandlerFunc(a.handleHost)))
	mux.Handle("DELETE /api/hosts/{id}", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleRemoveHost))))
	mux.Handle("POST /api/hosts/enrollment", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleEnrollment))))
}

func (a *API) handleHosts(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	hosts, err := st.ListHosts(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("list hosts", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		cs, err := st.ListContainers(r.Context(), p.OrgID, h.ID)
		if err != nil {
			a.log.Error("list containers", "err", err)
			writeError(w, http.StatusServiceUnavailable, "try again")
			return
		}
		running := 0
		for _, c := range cs {
			if c.State == "running" {
				running++
			}
		}
		out = append(out, hostJSON(h, running, len(cs)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

func (a *API) handleHost(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	h, ok := a.requireHost(w, r, st, p.OrgID, id)
	if !ok {
		return
	}
	cs, err := st.ListContainers(r.Context(), p.OrgID, h.ID)
	if err != nil {
		a.log.Error("list containers", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	running := 0
	for _, c := range cs {
		if c.State == "running" {
			running++
		}
	}
	var logStats any
	stats, err := st.GetLogStats(r.Context(), p.OrgID, h.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Error("get log stats", "err", err)
			writeError(w, http.StatusServiceUnavailable, "try again")
			return
		}
		logStats = nil
	} else {
		logStats = map[string]int64{
			"linesIn":           stats.LinesIn,
			"linesSkipped":      stats.LinesSkipped,
			"eventsOut":         stats.EventsOut,
			"redactionFailures": stats.RedactionFailures,
			"linesDropped":      stats.LinesDropped,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":     hostJSON(h, running, len(cs)),
		"logStats": logStats,
	})
}

func (a *API) handleContainers(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, id); !ok {
		return
	}
	cs, err := st.ListContainers(r.Context(), p.OrgID, id)
	if err != nil {
		a.log.Error("list containers", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		ports := c.Ports
		if ports == nil {
			ports = []store.Port{}
		}
		out = append(out, map[string]any{
			"id":           c.ContainerID,
			"name":         c.Name,
			"stack":        c.Stack,
			"service":      c.Service,
			"image":        c.Image,
			"imageVersion": c.ImageVersion,
			"state":        c.State,
			"status":       c.Status,
			"health":       c.Health,
			"restartCount": c.RestartCount,
			"oomKilled":    c.OOMKilled,
			"exitCode":     c.ExitCode,
			"ports":        ports,
			"createdAt":    rfc3339Ptr(c.CreatedAt),
			"startedAt":    rfc3339Ptr(c.StartedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"containers": out})
}

func (a *API) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, id); !ok {
		return
	}
	metric := r.URL.Query().Get("metric")
	if _, ok := hostMetricsAllow[metric]; !ok {
		writeError(w, http.StatusBadRequest, "unknown metric")
		return
	}
	from, to, ok := a.parseFromTo(w, r, time.Hour)
	if !ok {
		return
	}
	res, pts, err := st.HostSeries(r.Context(), p.OrgID, id, metric, from, to)
	if err != nil {
		a.log.Error("host series", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, seriesJSON(metric, res, pts))
}

func (a *API) handleContainerMetrics(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, id); !ok {
		return
	}
	cid := r.PathValue("cid")
	if cid == "" || len(cid) > 128 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	metric := r.URL.Query().Get("metric")
	if _, ok := containerMetricsAllow[metric]; !ok {
		writeError(w, http.StatusBadRequest, "unknown metric")
		return
	}
	from, to, ok := a.parseFromTo(w, r, time.Hour)
	if !ok {
		return
	}
	res, pts, err := st.ContainerSeries(r.Context(), p.OrgID, id, cid, metric, from, to)
	if err != nil {
		a.log.Error("container series", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, seriesJSON(metric, res, pts))
}

func (a *API) handleHostLogs(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, id); !ok {
		return
	}
	minLevel, ok := parseMinLevel(r.URL.Query().Get("minLevel"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid minLevel")
		return
	}
	from, to, ok := a.parseFromTo(w, r, 24*time.Hour)
	if !ok {
		return
	}
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	if limit > 500 {
		limit = 500
	}
	evs, err := st.ListLogEvents(r.Context(), p.OrgID, id, from, to, minLevel, limit)
	if err != nil {
		a.log.Error("list log events", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		out = append(out, map[string]any{
			"id":          e.ID,
			"containerId": e.ContainerID,
			"templateId":  e.TemplateID,
			"level":       levelName(e.Level),
			"sample":      e.Sample,
			"count":       e.Count,
			"firstSeen":   rfc3339(e.FirstTS),
			"lastSeen":    rfc3339(e.LastTS),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

func (a *API) handleEnrollment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		TTLHours *int   `json:"ttlHours"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if !hostNameRE.MatchString(in.Name) {
		writeError(w, http.StatusBadRequest, "invalid host name")
		return
	}
	ttlHours := 24
	if in.TTLHours != nil {
		ttlHours = *in.TTLHours
	}
	if ttlHours < 1 || ttlHours > 168 {
		writeError(w, http.StatusBadRequest, "ttlHours must be between 1 and 168")
		return
	}

	st, p := a.scoped(r)
	hosts, err := st.ListHosts(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("list hosts", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	for _, h := range hosts {
		if h.Name == in.Name {
			writeError(w, http.StatusConflict, "a host with that name already exists")
			return
		}
	}
	token, tok, err := st.CreateEnrollmentToken(r.Context(), p.OrgID, in.Name, time.Duration(ttlHours)*time.Hour)
	if err != nil {
		a.log.Error("create enrollment token", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"expiresAt": rfc3339(tok.ExpiresAt),
		"command":   a.installCommand(token),
		"host":      in.Name,
	})
}

// handleRemoveHost revokes a host: the agent is disconnected and refused from
// now on, and the host vanishes from lists. Containers on the host are not
// touched and no data is deleted.
func (a *API) handleRemoveHost(w http.ResponseWriter, r *http.Request) {
	_, p := a.scoped(r)
	id, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	// Revocation is a server-role write (the request role cannot update hosts).
	h, err := a.st.RevokeHost(r.Context(), p.OrgID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("revoke host", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	disconnected := false
	if a.cfg.HostDisconnecter != nil {
		disconnected = a.cfg.HostDisconnecter.DisconnectHost(id)
	}
	a.log.Info("audit: host removed",
		"org", p.OrgID, "user", p.UserID, "host_id", h.ID, "host", h.Name, "disconnected", disconnected)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) requireHost(w http.ResponseWriter, r *http.Request, st *store.Store, orgID, id uuid.UUID) (store.Host, bool) {
	h, err := st.GetHost(r.Context(), orgID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return store.Host{}, false
		}
		a.log.Error("get host", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return store.Host{}, false
	}
	return h, true
}

func parseHostID(w http.ResponseWriter, s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return uuid.UUID{}, false
	}
	return id, true
}

func hostJSON(h store.Host, running, total int) map[string]any {
	return map[string]any{
		"id":            h.ID.String(),
		"name":          h.Name,
		"hostname":      h.Info.Hostname,
		"os":            h.Info.OS,
		"kernel":        h.Info.Kernel,
		"arch":          h.Info.Arch,
		"dockerVersion": h.Info.DockerVersion,
		"agentVersion":  h.Info.AgentVersion,
		"connected":     h.Connected,
		"lastSeen":      rfc3339Ptr(h.LastSeenAt),
		"enrolledAt":    rfc3339(h.EnrolledAt),
		"containers":    map[string]int{"running": running, "total": total},
	}
}

func seriesJSON(metric string, res store.Resolution, pts []store.Point) map[string]any {
	out := make([]map[string]any, 0, len(pts))
	for _, p := range pts {
		out = append(out, map[string]any{
			"t":   p.TS.UTC().UnixMilli(),
			"v":   p.Value,
			"max": p.Max,
		})
	}
	return map[string]any{
		"metric":     metric,
		"resolution": string(res),
		"points":     out,
	}
}

func (a *API) parseFromTo(w http.ResponseWriter, r *http.Request, defaultSpan time.Duration) (from, to time.Time, ok bool) {
	now := a.now().UTC()
	to = now
	if s := r.URL.Query().Get("to"); s != "" {
		t, err := parseTimeParam(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid time range")
			return time.Time{}, time.Time{}, false
		}
		to = t
	}
	from = to.Add(-defaultSpan)
	if s := r.URL.Query().Get("from"); s != "" {
		t, err := parseTimeParam(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid time range")
			return time.Time{}, time.Time{}, false
		}
		from = t
	}
	if !from.Before(to) {
		writeError(w, http.StatusBadRequest, "invalid time range")
		return time.Time{}, time.Time{}, false
	}
	if to.Sub(from) > 400*24*time.Hour {
		writeError(w, http.StatusBadRequest, "invalid time range")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func parseTimeParam(s string) (time.Time, error) {
	if isAllDigits(s) {
		ms, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		return time.UnixMilli(ms).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}, err
		}
	}
	return t.UTC(), nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func rfc3339Ptr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return rfc3339(*t)
}

func levelName(l int16) string {
	if l >= 0 && int(l) < len(levelNames) {
		return levelNames[l]
	}
	return "unknown"
}

func parseMinLevel(s string) (int16, bool) {
	if s == "" {
		return store.LevelWarn, true
	}
	if s == "all" {
		return 0, true
	}
	for i, name := range levelNames {
		if name == s {
			return int16(i), true
		}
	}
	return 0, false
}
