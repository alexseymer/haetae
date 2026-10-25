package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

func (a *API) registerAlerts(mux *http.ServeMux) {
	mux.Handle("GET /api/incidents", a.authenticate(http.HandlerFunc(a.handleListIncidents)))
	mux.Handle("GET /api/incidents/{id}", a.authenticate(http.HandlerFunc(a.handleGetIncident)))
	mux.Handle("GET /api/alerts", a.authenticate(http.HandlerFunc(a.handleListAlerts)))
	mux.Handle("GET /api/settings/alerts", a.authenticate(http.HandlerFunc(a.handleGetAlertSettings)))
	mux.Handle("PUT /api/settings/alerts", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handlePutAlertSettings))))
	mux.Handle("GET /api/mutes", a.authenticate(http.HandlerFunc(a.handleListMutes)))
	mux.Handle("POST /api/mutes", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleCreateMute))))
	mux.Handle("DELETE /api/mutes/{id}", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleDeleteMute))))
}

func (a *API) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	state := r.URL.Query().Get("state")
	switch state {
	case "", store.IncidentOpen, store.IncidentResolved:
	default:
		writeError(w, http.StatusBadRequest, "state must be open or resolved")
		return
	}
	limit := parseLimitQuery(r, 50)
	var hostFilter uuid.UUID
	if hostStr := r.URL.Query().Get("hostId"); hostStr != "" {
		id, ok := parseHostID(w, hostStr)
		if !ok {
			return
		}
		if _, ok := a.requireHost(w, r, st, p.OrgID, id); !ok {
			return
		}
		hostFilter = id
	}
	fetchLimit := limit
	if hostFilter != uuid.Nil {
		fetchLimit = 200
	}
	list, err := st.ListIncidents(r.Context(), p.OrgID, state, fetchLimit)
	if err != nil {
		a.log.Error("list incidents", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, inc := range list {
		if hostFilter != uuid.Nil && inc.HostID != hostFilter {
			continue
		}
		hostName := ""
		if h, err := st.GetHost(r.Context(), p.OrgID, inc.HostID); err == nil {
			hostName = h.Name
		}
		_, alerts, err := st.GetIncident(r.Context(), p.OrgID, inc.ID)
		if err != nil {
			a.log.Error("get incident alerts", "err", err)
			writeError(w, http.StatusServiceUnavailable, "try again")
			return
		}
		row := incidentJSON(inc)
		row["hostName"] = hostName
		row["alertCount"] = len(alerts)
		out = append(out, row)
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": out})
}

func (a *API) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseUUIDParam(w, r.PathValue("id"))
	if !ok {
		return
	}
	inc, alerts, err := st.GetIncident(r.Context(), p.OrgID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get incident", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	alertOut := make([]map[string]any, 0, len(alerts))
	for _, al := range alerts {
		alertOut = append(alertOut, alertJSON(al))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"incident": incidentJSON(inc),
		"alerts":   alertOut,
	})
}

func (a *API) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	state := r.URL.Query().Get("state")
	switch state {
	case "", store.AlertFiring, store.AlertResolved:
	default:
		writeError(w, http.StatusBadRequest, "state must be firing or resolved")
		return
	}
	limit := parseLimitQuery(r, 50)
	list, err := st.ListAlerts(r.Context(), p.OrgID, state, limit)
	if err != nil {
		a.log.Error("list alerts", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, al := range list {
		out = append(out, alertJSON(al))
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": out})
}

func (a *API) handleGetAlertSettings(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	s, err := st.GetOrgSettings(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("get org settings", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, orgSettingsJSON(s))
}

func (a *API) handlePutAlertSettings(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	var in struct {
		TimeZone      string `json:"timeZone"`
		BriefingHour  int    `json:"briefingHour"`
		CPUPct        int    `json:"cpuPct"`
		MemPct        int    `json:"memPct"`
		DiskPct       int    `json:"diskPct"`
		SustainedS    int    `json:"sustainedS"`
		OfflineAfterS int    `json:"offlineAfterS"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := validateOrgSettings(in.TimeZone, in.BriefingHour, in.CPUPct, in.MemPct, in.DiskPct, in.SustainedS, in.OfflineAfterS); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := st.UpsertOrgSettings(r.Context(), store.OrgSettings{
		OrgID:         p.OrgID,
		TimeZone:      in.TimeZone,
		BriefingHour:  in.BriefingHour,
		CPUPct:        in.CPUPct,
		MemPct:        in.MemPct,
		DiskPct:       in.DiskPct,
		SustainedS:    in.SustainedS,
		OfflineAfterS: in.OfflineAfterS,
	})
	if err != nil {
		a.log.Error("upsert org settings", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, orgSettingsJSON(saved))
}

func (a *API) handleListMutes(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	list, err := st.ActiveMutes(r.Context(), p.OrgID, a.now())
	if err != nil {
		a.log.Error("list mutes", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		out = append(out, muteJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mutes": out})
}

func (a *API) handleCreateMute(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	var in struct {
		HostID *string `json:"hostId"`
		Rule   string  `json:"rule"`
		Days   int     `json:"days"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Days < 1 || in.Days > 90 {
		writeError(w, http.StatusBadRequest, "days must be between 1 and 90")
		return
	}
	if in.Rule != "" && !validMuteRule(in.Rule) {
		writeError(w, http.StatusBadRequest, "unknown rule")
		return
	}
	var hostID *uuid.UUID
	if in.HostID != nil && *in.HostID != "" {
		id, ok := parseHostID(w, *in.HostID)
		if !ok {
			return
		}
		if _, ok := a.requireHost(w, r, st, p.OrgID, id); !ok {
			return
		}
		hostID = &id
	}
	uid := p.UserID
	m, err := st.CreateMute(r.Context(), store.Mute{
		OrgID:     p.OrgID,
		HostID:    hostID,
		Rule:      in.Rule,
		Until:     a.now().UTC().Add(time.Duration(in.Days) * 24 * time.Hour),
		CreatedBy: &uid,
	})
	if err != nil {
		a.log.Error("create mute", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"mute": muteJSON(m)})
}

func (a *API) handleDeleteMute(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseUUIDParam(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := st.DeleteMute(r.Context(), p.OrgID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("delete mute", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateOrgSettings(tz string, briefingHour, cpu, mem, disk, sustained, offline int) error {
	if tz == "" {
		return errors.New("time zone is required")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return errors.New("invalid time zone")
	}
	if briefingHour < 0 || briefingHour > 23 {
		return errors.New("briefingHour must be between 0 and 23")
	}
	if cpu < 1 || cpu > 100 {
		return errors.New("cpuPct must be between 1 and 100")
	}
	if mem < 1 || mem > 100 {
		return errors.New("memPct must be between 1 and 100")
	}
	if disk < 1 || disk > 100 {
		return errors.New("diskPct must be between 1 and 100")
	}
	if sustained < 10 || sustained > 86400 {
		return errors.New("sustainedS must be between 10 and 86400")
	}
	if offline < 30 || offline > 86400 {
		return errors.New("offlineAfterS must be between 30 and 86400")
	}
	return nil
}

func validMuteRule(rule string) bool {
	switch rule {
	case store.RuleCrashLoop, store.RuleOOMKill, store.RuleUnhealthy, store.RuleHostOffline,
		store.RuleCPUHigh, store.RuleMemHigh, store.RuleDiskHigh, store.RuleDeployFailed, store.RuleNewCVE:
		return true
	}
	return false
}

func parseLimitQuery(r *http.Request, def int) int {
	limit := def
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	return limit
}

func incidentJSON(inc store.Incident) map[string]any {
	return map[string]any{
		"id": inc.ID.String(), "orgId": inc.OrgID.String(), "hostId": inc.HostID.String(),
		"title": inc.Title, "severity": inc.Severity, "state": inc.State,
		"openedAt": rfc3339(inc.OpenedAt), "updatedAt": rfc3339(inc.UpdatedAt),
		"resolvedAt": rfc3339Ptr(inc.ResolvedAt),
	}
}

func alertJSON(al store.Alert) map[string]any {
	ev := al.Evidence
	if len(ev) == 0 {
		ev = []byte("{}")
	}
	return map[string]any{
		"id": al.ID.String(), "orgId": al.OrgID.String(), "hostId": al.HostID.String(),
		"incidentId": al.IncidentID.String(), "rule": al.Rule, "container": al.Container,
		"severity": al.Severity, "state": al.State, "dedupeKey": al.DedupeKey,
		"summary": al.Summary, "evidence": ev,
		"firstAt": rfc3339(al.FirstAt), "lastAt": rfc3339(al.LastAt),
		"resolvedAt": rfc3339Ptr(al.ResolvedAt),
	}
}

func orgSettingsJSON(s store.OrgSettings) map[string]any {
	out := map[string]any{
		"orgId": s.OrgID.String(), "timeZone": s.TimeZone, "briefingHour": s.BriefingHour,
		"cpuPct": s.CPUPct, "memPct": s.MemPct, "diskPct": s.DiskPct,
		"sustainedS": s.SustainedS, "offlineAfterS": s.OfflineAfterS,
	}
	if !s.UpdatedAt.IsZero() {
		out["updatedAt"] = rfc3339(s.UpdatedAt)
	}
	return out
}

func muteJSON(m store.Mute) map[string]any {
	out := map[string]any{
		"id": m.ID.String(), "orgId": m.OrgID.String(), "rule": m.Rule,
		"until": rfc3339(m.Until), "createdAt": rfc3339(m.CreatedAt),
	}
	if m.HostID != nil {
		out["hostId"] = m.HostID.String()
	}
	if m.CreatedBy != nil {
		out["createdBy"] = m.CreatedBy.String()
	}
	return out
}
