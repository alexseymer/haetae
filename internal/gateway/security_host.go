package gateway

import (
	"context"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/updates"
)

const (
	maxMisconfigsPerReport = 2000
	maxOutboundPerBatch    = 500
	maxImageCandidates     = 500
	maxDetailLen           = 256
	securityReportRate     = 2 // per second
	outboundBatchRate      = 2
	imageCandidatesRate    = 1
)

var misconfigKinds = map[string]bool{
	"published_port": true, "docker_socket": true, "root_user": true,
	"privileged": true, "host_network": true, "host_pid": true,
	"dangerous_capability": true, "cert_expiring": true,
}

var misconfigSev = map[string]bool{
	"low": true, "medium": true, "high": true, "critical": true,
}

var conntrackStates = map[string]bool{
	"ok": true, "degraded": true, "unavailable": true, "unknown": true,
}

var imageStatuses = map[string]bool{
	"ok": true, "unknown": true, "error": true,
}

// securityStore is the persistence used by host security handlers.
type securityStore interface {
	ReplaceMisconfigs(ctx context.Context, orgID, hostID uuid.UUID, ms []store.Misconfig) error
	UpsertHostSecurity(ctx context.Context, hs store.HostSecurity) error
	UpsertOutbound(ctx context.Context, orgID, hostID uuid.UUID, conns []store.OutboundConn) error
	UpsertImageUpdate(ctx context.Context, u store.ImageUpdate) error
	ContainerBelongs(ctx context.Context, orgID, hostID uuid.UUID, containerID string) (bool, error)
}

func (g *Gateway) sec() securityStore {
	if g.store == nil {
		return nil
	}
	return g.store
}

func (g *Gateway) handleSecurityReport(ctx context.Context, s *session, r *agentv1.SecurityReport) {
	if r == nil {
		return
	}
	st := g.sec()
	if st == nil {
		return
	}
	if s.securityLim != nil && !s.securityLim.Allow() {
		s.droppedSecurity.Add(1)
		return
	}
	ms := r.GetMisconfigs()
	if len(ms) > maxMisconfigsPerReport {
		ms = ms[:maxMisconfigsPerReport]
	}
	out := make([]store.Misconfig, 0, len(ms))
	for _, m := range ms {
		kind := clip(m.GetKind())
		sev := clip(m.GetSeverity())
		if !misconfigKinds[kind] || !misconfigSev[sev] {
			continue
		}
		cid := clip(m.GetContainerId())
		if cid != "" {
			ok, err := st.ContainerBelongs(ctx, s.orgID, s.hostID, cid)
			if err != nil || !ok {
				continue
			}
		}
		out = append(out, store.Misconfig{
			ContainerID: cid,
			Kind:        kind,
			Severity:    sev,
			Detail:      clipLen(m.GetDetail(), maxDetailLen),
		})
	}
	if err := st.ReplaceMisconfigs(ctx, s.orgID, s.hostID, out); err != nil {
		g.log.Error("store misconfigs", "host_id", s.hostID, "err", err)
	}
	if ct := r.GetConntrack(); ct != nil {
		state := clip(ct.GetState())
		if !conntrackStates[state] {
			state = "unknown"
		}
		notMon := ct.GetNotMonitoredContainerIds()
		if len(notMon) > 500 {
			notMon = notMon[:500]
		}
		clipped := make([]string, 0, len(notMon))
		for _, id := range notMon {
			clipped = append(clipped, clip(id))
		}
		if err := st.UpsertHostSecurity(ctx, store.HostSecurity{
			HostID:          s.hostID,
			OrgID:           s.orgID,
			ConntrackState:  state,
			ConntrackReason: clipLen(ct.GetReason(), maxDetailLen),
			NotMonitored:    clipped,
		}); err != nil {
			g.log.Error("store host security", "host_id", s.hostID, "err", err)
		}
	}
}

func (g *Gateway) handleOutboundBatch(ctx context.Context, s *session, b *agentv1.OutboundBatch) {
	if b == nil {
		return
	}
	st := g.sec()
	if st == nil {
		return
	}
	if s.outboundLim != nil && !s.outboundLim.Allow() {
		s.droppedSecurity.Add(1)
		return
	}
	cs := b.GetConns()
	if len(cs) > maxOutboundPerBatch {
		cs = cs[:maxOutboundPerBatch]
	}
	out := make([]store.OutboundConn, 0, len(cs))
	for _, c := range cs {
		cid := clip(c.GetContainerId())
		if cid == "" {
			continue
		}
		ok, err := st.ContainerBelongs(ctx, s.orgID, s.hostID, cid)
		if err != nil || !ok {
			continue
		}
		proto := clip(c.GetProto())
		if proto != "tcp" && proto != "udp" {
			continue
		}
		port := int(c.GetDstPort())
		if port <= 0 || port > 65535 {
			continue
		}
		out = append(out, store.OutboundConn{
			ContainerID: cid,
			DstIP:       clip(c.GetDstIp()),
			DstPort:     port,
			Proto:       proto,
			Count:       int64(c.GetCount()),
		})
	}
	if len(out) == 0 {
		return
	}
	if err := st.UpsertOutbound(ctx, s.orgID, s.hostID, out); err != nil {
		g.log.Error("store outbound", "host_id", s.hostID, "err", err)
	}
}

func (g *Gateway) handleImageCandidates(ctx context.Context, s *session, c *agentv1.ImageCandidates) {
	if c == nil {
		return
	}
	st := g.sec()
	if st == nil {
		return
	}
	if s.imageCandLim != nil && !s.imageCandLim.Allow() {
		s.droppedSecurity.Add(1)
		return
	}
	cands := c.GetCandidates()
	if len(cands) > maxImageCandidates {
		cands = cands[:maxImageCandidates]
	}
	now := time.Now().UTC()
	for _, cand := range cands {
		cid := clip(cand.GetContainerId())
		if cid == "" {
			continue
		}
		ok, err := st.ContainerBelongs(ctx, s.orgID, s.hostID, cid)
		if err != nil || !ok {
			continue
		}
		status := clip(cand.GetStatus())
		if status != "" && !imageStatuses[status] {
			status = "unknown"
		}
		// Length-clip agent text; UI escapes on display.
		cand.ContainerId = cid
		cand.Image = clip(cand.GetImage())
		cand.RunningDigest = clip(cand.GetRunningDigest())
		cand.CandidateDigest = clip(cand.GetCandidateDigest())
		cand.Status = status
		cand.Error = clipLen(cand.GetError(), maxDetailLen)

		u := updates.ApplyCandidate(s.orgID, s.hostID, cand, g.findings, now)
		if err := st.UpsertImageUpdate(ctx, u); err != nil {
			g.log.Error("store image update", "host_id", s.hostID, "err", err)
		}
	}
}

func clipLen(s string, n int) string {
	if len(s) <= n {
		return clip(s)
	}
	return clip(s[:n])
}
