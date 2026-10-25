package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Misconfig is one host security finding.
type Misconfig struct {
	OrgID       uuid.UUID
	HostID      uuid.UUID
	ContainerID string
	Kind        string
	Severity    string
	Detail      string
	FirstSeen   time.Time
	LastSeen    time.Time
	ResolvedAt  *time.Time
}

// HostSecurity is the conntrack monitoring state for a host.
type HostSecurity struct {
	HostID          uuid.UUID
	OrgID           uuid.UUID
	ConntrackState  string
	ConntrackReason string
	NotMonitored    []string
	UpdatedAt       time.Time
}

// OutboundConn is one aggregated outbound destination.
type OutboundConn struct {
	OrgID       uuid.UUID
	HostID      uuid.UUID
	ContainerID string
	DstIP       string
	DstPort     int
	Proto       string
	FirstSeen   time.Time
	LastSeen    time.Time
	Count       int64
}

// ImageUpdate is the server's decision for one container's image.
type ImageUpdate struct {
	OrgID            uuid.UUID
	HostID           uuid.UUID
	ContainerID      string
	Image            string
	RunningDigest    string
	CandidateDigest  string
	CandidateCreated *time.Time
	Decision         string
	Reason           string
	DecidedAt        time.Time
}

const (
	maxOutboundRowsPerHost = 5000
	outboundRetentionDays  = 30
)

// ReplaceMisconfigs upserts the current findings (preserving first_seen) and
// sets resolved_at on rows for the host that are absent from ms.
func (s *Store) ReplaceMisconfigs(ctx context.Context, orgID, hostID uuid.UUID, ms []Misconfig) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: ReplaceMisconfigs: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	type key struct{ cid, kind, detail string }
	keep := make([]key, 0, len(ms))
	for _, m := range ms {
		_, err := tx.Exec(ctx, `
			INSERT INTO misconfigs (org_id, host_id, container_id, kind, severity, detail, first_seen, last_seen, resolved_at)
			VALUES ($1,$2,$3,$4,$5,$6,now(),now(),NULL)
			ON CONFLICT (host_id, container_id, kind, detail) DO UPDATE SET
				org_id = EXCLUDED.org_id,
				severity = EXCLUDED.severity,
				last_seen = now(),
				resolved_at = NULL,
				first_seen = misconfigs.first_seen`,
			orgID, hostID, m.ContainerID, m.Kind, m.Severity, m.Detail,
		)
		if err != nil {
			return fmt.Errorf("store: ReplaceMisconfigs: %w", err)
		}
		keep = append(keep, key{m.ContainerID, m.Kind, m.Detail})
	}

	// Mark absent open findings resolved.
	rows, err := tx.Query(ctx, `
		SELECT container_id, kind, detail FROM misconfigs
		WHERE org_id = $1 AND host_id = $2 AND resolved_at IS NULL`, orgID, hostID)
	if err != nil {
		return fmt.Errorf("store: ReplaceMisconfigs: %w", err)
	}
	var toResolve []key
	present := make(map[key]bool, len(keep))
	for _, k := range keep {
		present[k] = true
	}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.cid, &k.kind, &k.detail); err != nil {
			rows.Close()
			return fmt.Errorf("store: ReplaceMisconfigs: %w", err)
		}
		if !present[k] {
			toResolve = append(toResolve, k)
		}
	}
	rows.Close()
	for _, k := range toResolve {
		_, err := tx.Exec(ctx, `
			UPDATE misconfigs SET resolved_at = now(), last_seen = now()
			WHERE org_id = $1 AND host_id = $2 AND container_id = $3 AND kind = $4 AND detail = $5
			  AND resolved_at IS NULL`,
			orgID, hostID, k.cid, k.kind, k.detail)
		if err != nil {
			return fmt.Errorf("store: ReplaceMisconfigs: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: ReplaceMisconfigs: %w", err)
	}
	return nil
}

// UpsertHostSecurity stores conntrack monitoring state for a host.
func (s *Store) UpsertHostSecurity(ctx context.Context, hs HostSecurity) error {
	notMon, err := json.Marshal(hs.NotMonitored)
	if err != nil {
		return fmt.Errorf("store: UpsertHostSecurity: %w", err)
	}
	if notMon == nil {
		notMon = []byte("[]")
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO host_security (host_id, org_id, conntrack_state, conntrack_reason, not_monitored, updated_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (host_id) DO UPDATE SET
			org_id = EXCLUDED.org_id,
			conntrack_state = EXCLUDED.conntrack_state,
			conntrack_reason = EXCLUDED.conntrack_reason,
			not_monitored = EXCLUDED.not_monitored,
			updated_at = now()`,
		hs.HostID, hs.OrgID, hs.ConntrackState, hs.ConntrackReason, notMon,
	)
	if err != nil {
		return fmt.Errorf("store: UpsertHostSecurity: %w", err)
	}
	return nil
}

// UpsertOutbound upserts a batch of outbound rows and trims per-host and age.
func (s *Store) UpsertOutbound(ctx context.Context, orgID, hostID uuid.UUID, conns []OutboundConn) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: UpsertOutbound: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, c := range conns {
		_, err := tx.Exec(ctx, `
			INSERT INTO outbound_conns (org_id, host_id, container_id, dst_ip, dst_port, proto, first_seen, last_seen, count)
			VALUES ($1,$2,$3,$4,$5,$6,now(),now(),$7)
			ON CONFLICT (host_id, container_id, dst_ip, dst_port, proto) DO UPDATE SET
				org_id = EXCLUDED.org_id,
				last_seen = now(),
				count = outbound_conns.count + EXCLUDED.count`,
			orgID, hostID, c.ContainerID, c.DstIP, c.DstPort, c.Proto, c.Count,
		)
		if err != nil {
			return fmt.Errorf("store: UpsertOutbound: %w", err)
		}
	}
	// Bound rows per host: drop oldest last_seen beyond the cap.
	_, err = tx.Exec(ctx, `
		DELETE FROM outbound_conns
		WHERE host_id = $1 AND ctid IN (
			SELECT ctid FROM outbound_conns WHERE host_id = $1
			ORDER BY last_seen DESC OFFSET $2
		)`, hostID, maxOutboundRowsPerHost)
	if err != nil {
		return fmt.Errorf("store: UpsertOutbound: %w", err)
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM outbound_conns
		WHERE host_id = $1 AND last_seen < now() - make_interval(days => $2)`,
		hostID, outboundRetentionDays)
	if err != nil {
		return fmt.Errorf("store: UpsertOutbound: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: UpsertOutbound: %w", err)
	}
	return nil
}

// PurgeOutboundOlderThan removes outbound rows older than the retention window
// across all hosts (maintenance).
func (s *Store) PurgeOutboundOlderThan(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		days = outboundRetentionDays
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM outbound_conns WHERE last_seen < now() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, fmt.Errorf("store: PurgeOutboundOlderThan: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpsertImageUpdate stores a decision for one container.
func (s *Store) UpsertImageUpdate(ctx context.Context, u ImageUpdate) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO image_updates (
			org_id, host_id, container_id, image, running_digest, candidate_digest,
			candidate_created, decision, reason, decided_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		ON CONFLICT (host_id, container_id) DO UPDATE SET
			org_id = EXCLUDED.org_id,
			image = EXCLUDED.image,
			running_digest = EXCLUDED.running_digest,
			candidate_digest = EXCLUDED.candidate_digest,
			candidate_created = EXCLUDED.candidate_created,
			decision = EXCLUDED.decision,
			reason = EXCLUDED.reason,
			decided_at = now()`,
		u.OrgID, u.HostID, u.ContainerID, u.Image, u.RunningDigest, u.CandidateDigest,
		u.CandidateCreated, u.Decision, u.Reason,
	)
	if err != nil {
		return fmt.Errorf("store: UpsertImageUpdate: %w", err)
	}
	return nil
}

// ListMisconfigs returns findings for a host (org-scoped). openOnly skips resolved.
func (s *Store) ListMisconfigs(ctx context.Context, orgID, hostID uuid.UUID, openOnly bool) ([]Misconfig, error) {
	q := `
		SELECT org_id, host_id, container_id, kind, severity, detail, first_seen, last_seen, resolved_at
		FROM misconfigs WHERE org_id = $1 AND host_id = $2`
	if openOnly {
		q += ` AND resolved_at IS NULL`
	}
	q += ` ORDER BY severity DESC, first_seen ASC`
	rows, err := s.pool.Query(ctx, q, orgID, hostID)
	if err != nil {
		return nil, fmt.Errorf("store: ListMisconfigs: %w", err)
	}
	defer rows.Close()
	var out []Misconfig
	for rows.Next() {
		var m Misconfig
		if err := rows.Scan(&m.OrgID, &m.HostID, &m.ContainerID, &m.Kind, &m.Severity, &m.Detail,
			&m.FirstSeen, &m.LastSeen, &m.ResolvedAt); err != nil {
			return nil, fmt.Errorf("store: ListMisconfigs: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetHostSecurity returns conntrack state for a host.
func (s *Store) GetHostSecurity(ctx context.Context, orgID, hostID uuid.UUID) (HostSecurity, error) {
	var hs HostSecurity
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT host_id, org_id, conntrack_state, conntrack_reason, not_monitored, updated_at
		FROM host_security WHERE org_id = $1 AND host_id = $2`, orgID, hostID,
	).Scan(&hs.HostID, &hs.OrgID, &hs.ConntrackState, &hs.ConntrackReason, &raw, &hs.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HostSecurity{}, ErrNotFound
		}
		return HostSecurity{}, fmt.Errorf("store: GetHostSecurity: %w", err)
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &hs.NotMonitored)
	}
	return hs, nil
}

// ListOutbound returns recent outbound rows for a host.
func (s *Store) ListOutbound(ctx context.Context, orgID, hostID uuid.UUID, limit int) ([]OutboundConn, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, host_id, container_id, dst_ip, dst_port, proto, first_seen, last_seen, count
		FROM outbound_conns WHERE org_id = $1 AND host_id = $2
		ORDER BY last_seen DESC LIMIT $3`, orgID, hostID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: ListOutbound: %w", err)
	}
	defer rows.Close()
	var out []OutboundConn
	for rows.Next() {
		var c OutboundConn
		if err := rows.Scan(&c.OrgID, &c.HostID, &c.ContainerID, &c.DstIP, &c.DstPort, &c.Proto,
			&c.FirstSeen, &c.LastSeen, &c.Count); err != nil {
			return nil, fmt.Errorf("store: ListOutbound: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListImageUpdates returns image update decisions for a host.
func (s *Store) ListImageUpdates(ctx context.Context, orgID, hostID uuid.UUID) ([]ImageUpdate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, host_id, container_id, image, running_digest, candidate_digest,
			candidate_created, decision, reason, decided_at
		FROM image_updates WHERE org_id = $1 AND host_id = $2
		ORDER BY decided_at DESC`, orgID, hostID)
	if err != nil {
		return nil, fmt.Errorf("store: ListImageUpdates: %w", err)
	}
	defer rows.Close()
	var out []ImageUpdate
	for rows.Next() {
		var u ImageUpdate
		if err := rows.Scan(&u.OrgID, &u.HostID, &u.ContainerID, &u.Image, &u.RunningDigest, &u.CandidateDigest,
			&u.CandidateCreated, &u.Decision, &u.Reason, &u.DecidedAt); err != nil {
			return nil, fmt.Errorf("store: ListImageUpdates: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListImageUpdatesByDecision returns updates matching decision across an org.
func (s *Store) ListImageUpdatesByDecision(ctx context.Context, orgID uuid.UUID, decisions []string) ([]ImageUpdate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, host_id, container_id, image, running_digest, candidate_digest,
			candidate_created, decision, reason, decided_at
		FROM image_updates WHERE org_id = $1 AND decision = ANY($2)
		ORDER BY decided_at ASC`, orgID, decisions)
	if err != nil {
		return nil, fmt.Errorf("store: ListImageUpdatesByDecision: %w", err)
	}
	defer rows.Close()
	var out []ImageUpdate
	for rows.Next() {
		var u ImageUpdate
		if err := rows.Scan(&u.OrgID, &u.HostID, &u.ContainerID, &u.Image, &u.RunningDigest, &u.CandidateDigest,
			&u.CandidateCreated, &u.Decision, &u.Reason, &u.DecidedAt); err != nil {
			return nil, fmt.Errorf("store: ListImageUpdatesByDecision: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// OrgAutoPull returns org_settings.auto_pull (false when no row).
func (s *Store) OrgAutoPull(ctx context.Context, orgID uuid.UUID) (bool, error) {
	var v bool
	err := s.pool.QueryRow(ctx, `SELECT auto_pull FROM org_settings WHERE org_id = $1`, orgID).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("store: OrgAutoPull: %w", err)
	}
	return v, nil
}

// SetOrgAutoPull sets auto_pull for tests and the updates loop.
func (s *Store) SetOrgAutoPull(ctx context.Context, orgID uuid.UUID, autoPull bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO org_settings (org_id, auto_pull) VALUES ($1, $2)
		ON CONFLICT (org_id) DO UPDATE SET auto_pull = EXCLUDED.auto_pull, updated_at = now()`,
		orgID, autoPull)
	if err != nil {
		return fmt.Errorf("store: SetOrgAutoPull: %w", err)
	}
	return nil
}

// ContainerBelongs reports whether containerID is currently on the host.
// Empty containerID is allowed (host-level findings such as cert_expiring).
func (s *Store) ContainerBelongs(ctx context.Context, orgID, hostID uuid.UUID, containerID string) (bool, error) {
	return s.ContainerBelongsToHost(ctx, orgID, hostID, containerID)
}
