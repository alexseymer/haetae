package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/deploys"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

const scanRateLimit = time.Minute

func (a *API) registerSecurity(mux *http.ServeMux) {
	mux.Handle("GET /api/hosts/{id}/security", a.authenticate(http.HandlerFunc(a.handleHostSecurity)))
	mux.Handle("GET /api/hosts/{id}/vulnerabilities", a.authenticate(http.HandlerFunc(a.handleHostVulnerabilities)))
	mux.Handle("POST /api/hosts/{id}/containers/{cid}/scan", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleForceScan))))
	mux.Handle("GET /api/hosts/{id}/outbound", a.authenticate(http.HandlerFunc(a.handleHostOutbound)))
	mux.Handle("GET /api/hosts/{id}/image-updates", a.authenticate(http.HandlerFunc(a.handleHostImageUpdates)))
	mux.Handle("GET /api/settings/updates", a.authenticate(http.HandlerFunc(a.handleGetUpdateSettings)))
	mux.Handle("PUT /api/settings/updates", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handlePutUpdateSettings))))
}

func (a *API) handleHostSecurity(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	hostID, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
		return
	}

	hs, err := st.GetHostSecurity(r.Context(), p.OrgID, hostID)
	conntrack := map[string]any{
		"state":        "unavailable",
		"reason":       "no report yet",
		"notMonitored": []string{},
	}
	if err == nil {
		notMon := hs.NotMonitored
		if notMon == nil {
			notMon = []string{}
		}
		conntrack = map[string]any{
			"state":        hs.ConntrackState,
			"reason":       hs.ConntrackReason,
			"notMonitored": notMon,
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		a.log.Error("get host security", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}

	ms, err := st.ListMisconfigs(r.Context(), p.OrgID, hostID, true)
	if err != nil {
		a.log.Error("list misconfigs", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	nameByID := containerNames(r.Context(), st, p.OrgID, hostID)
	misOut := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		misOut = append(misOut, map[string]any{
			"containerId":   m.ContainerID,
			"containerName": nameByID[m.ContainerID],
			"kind":          m.Kind,
			"severity":      m.Severity,
			"detail":        m.Detail,
			"firstSeen":     rfc3339(m.FirstSeen),
			"lastSeen":      rfc3339(m.LastSeen),
		})
	}

	badge, err := st.HostVulnBadgeSummary(r.Context(), p.OrgID, hostID)
	if err != nil {
		a.log.Error("host vuln summary", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conntrack":  conntrack,
		"misconfigs": misOut,
		"summary": map[string]any{
			"critical": badge.Critical,
			"high":     badge.High,
			"medium":   badge.Medium,
			"low":      badge.Low,
		},
		"scanFailed":   badge.ScanFailed,
		"matchPending": badge.Pending,
	})
}

func (a *API) handleHostVulnerabilities(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	hostID, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
		return
	}

	views, err := st.ContainerVulnViews(r.Context(), p.OrgID, hostID)
	if err != nil {
		a.log.Error("container vuln views", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	containers := make([]map[string]any, 0, len(views))
	for _, v := range views {
		findings := make([]map[string]any, 0, len(v.Findings))
		for _, f := range v.Findings {
			urls := f.URLs
			if urls == nil {
				urls = []string{}
			}
			findings = append(findings, map[string]any{
				"vulnId":       f.VulnID,
				"package":      f.Package,
				"version":      f.Version,
				"fixedVersion": f.FixedVersion,
				"severity":     f.Severity,
				"description":  f.Description,
				"urls":         urls,
			})
		}
		matchStatus := v.MatchStatus
		if matchStatus == "" {
			matchStatus = "none"
		}
		containers = append(containers, map[string]any{
			"containerId": v.ContainerID,
			"name":        v.Name,
			"image":       v.Image,
			"imageId":     v.ImageID,
			"scan": map[string]any{
				"status": v.ScanStatus,
				"error":  v.ScanError,
				"at":     rfc3339Ptr(v.ScanAt),
				"tool":   v.ToolVersion,
			},
			"match": map[string]any{
				"status":    matchStatus,
				"error":     v.MatchError,
				"dbBuiltAt": rfc3339Ptr(v.DBBuiltAt),
			},
			"counts": map[string]any{
				"critical": v.Counts.Critical,
				"high":     v.Counts.High,
				"medium":   v.Counts.Medium,
				"low":      v.Counts.Low,
				"unknown":  v.Counts.Unknown + v.Counts.Negligible,
			},
			"findings": findings,
		})
	}

	vdb, err := st.GetVulnDB(r.Context())
	if err != nil {
		a.log.Error("get vuln db", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	configured := vdb.Status != store.VulnDBDisabled && vdb.Status != store.VulnDBNone
	db := map[string]any{
		"status":     vdb.Status,
		"builtAt":    rfc3339Ptr(vdb.BuiltAt),
		"checkedAt":  rfc3339Ptr(vdb.CheckedAt),
		"error":      vdb.Error,
		"configured": configured,
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"containers": containers,
		"db":         db,
	})
}

func (a *API) handleForceScan(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	hostID, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
		return
	}
	cid := r.PathValue("cid")
	if cid == "" || len(cid) > 128 {
		writeError(w, http.StatusBadRequest, "invalid container id")
		return
	}
	belongs, err := st.ContainerBelongsToHost(r.Context(), p.OrgID, hostID, cid)
	if err != nil {
		a.log.Error("container belongs", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	if !belongs {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// Empty body is required for CSRF (JSON content-type); accept {}.
	var in struct{}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &in) {
			return
		}
	}

	key := hostID.String() + "|" + cid
	if !a.allowScan(key) {
		writeError(w, http.StatusTooManyRequests, "scan already requested; try again in a minute")
		return
	}

	sender := a.commandSender()
	if sender == nil || !sender.Connected(hostID) {
		a.clearScan(key)
		writeError(w, http.StatusServiceUnavailable, "host is not connected")
		return
	}

	scanID := uuid.New().String()
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_ScanContainer{
			ScanContainer: &agentv1.ScanContainer{
				ScanId:      scanID,
				ContainerId: cid,
			},
		},
	}
	sendCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := sender.Send(sendCtx, hostID, cmd)
	if err != nil {
		a.clearScan(key)
		if errors.Is(err, deploys.ErrNotDelivered) {
			writeError(w, http.StatusServiceUnavailable, "host is not connected")
			return
		}
		a.log.Error("send scan command", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	if res != nil && res.GetError() != "" {
		a.clearScan(key)
		writeError(w, http.StatusBadRequest, res.GetError())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"scanId":   scanID,
	})
}

func (a *API) handleHostOutbound(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	hostID, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
		return
	}
	limit := parseLimitQuery(r, 200)
	conns, err := st.ListOutbound(r.Context(), p.OrgID, hostID, limit)
	if err != nil {
		a.log.Error("list outbound", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	nameByID := containerNames(r.Context(), st, p.OrgID, hostID)
	out := make([]map[string]any, 0, len(conns))
	for _, c := range conns {
		out = append(out, map[string]any{
			"containerId":   c.ContainerID,
			"containerName": nameByID[c.ContainerID],
			"dstIp":         c.DstIP,
			"dstPort":       c.DstPort,
			"proto":         c.Proto,
			"count":         c.Count,
			"firstSeen":     rfc3339(c.FirstSeen),
			"lastSeen":      rfc3339(c.LastSeen),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"conns": out})
}

func (a *API) handleHostImageUpdates(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	hostID, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
		return
	}
	ups, err := st.ListImageUpdates(r.Context(), p.OrgID, hostID)
	if err != nil {
		a.log.Error("list image updates", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	nameByID := containerNames(r.Context(), st, p.OrgID, hostID)
	out := make([]map[string]any, 0, len(ups))
	for _, u := range ups {
		out = append(out, map[string]any{
			"containerId":      u.ContainerID,
			"containerName":    nameByID[u.ContainerID],
			"image":            u.Image,
			"runningDigest":    u.RunningDigest,
			"candidateDigest":  u.CandidateDigest,
			"candidateCreated": rfc3339Ptr(u.CandidateCreated),
			"decision":         u.Decision,
			"reason":           u.Reason,
			"decidedAt":        rfc3339(u.DecidedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"updates": out})
}

func (a *API) handleGetUpdateSettings(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	autoPull, err := st.OrgAutoPull(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("get auto pull", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"autoPull": autoPull})
}

func (a *API) handlePutUpdateSettings(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	var in struct {
		AutoPull bool `json:"autoPull"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := st.SetOrgAutoPull(r.Context(), p.OrgID, in.AutoPull); err != nil {
		a.log.Error("set auto pull", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"autoPull": in.AutoPull})
}

func containerNames(ctx context.Context, st *store.Store, orgID, hostID uuid.UUID) map[string]string {
	out := map[string]string{}
	cs, err := st.ListContainers(ctx, orgID, hostID)
	if err != nil {
		return out
	}
	for _, c := range cs {
		out[c.ContainerID] = c.Name
	}
	return out
}

// commandSender returns the agent command sender (forced scans reuse deploy delivery).
func (a *API) commandSender() deploys.Sender {
	if a.cfg.Sender != nil {
		return a.cfg.Sender
	}
	if a.cfg.Deploys != nil {
		return a.cfg.Deploys.Sender
	}
	return nil
}

func (a *API) allowScan(key string) bool {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.scanLast == nil {
		a.scanLast = map[string]time.Time{}
	}
	now := a.now()
	// Evict entries past the rate window so the map cannot grow without bound.
	for k, t := range a.scanLast {
		if now.Sub(t) >= scanRateLimit {
			delete(a.scanLast, k)
		}
	}
	if t, ok := a.scanLast[key]; ok && now.Sub(t) < scanRateLimit {
		return false
	}
	a.scanLast[key] = now
	return true
}

func (a *API) clearScan(key string) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	delete(a.scanLast, key)
}
