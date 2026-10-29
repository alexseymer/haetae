package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Queries behind the AI digests (log summaries and the security digest).
// Everything is scoped by organisation.

// LogDigestContainer is one container's share of a log group.
type LogDigestContainer struct {
	ContainerID string
	Name        string
	Count       int64
}

// LogDigestGroup is one log template of one host within a window, with the
// count of the previous window of equal length.
type LogDigestGroup struct {
	HostID     uuid.UUID
	HostName   string
	TemplateID string
	Level      int16
	Template   string
	Count      int64 // lines in the window
	PrevCount  int64 // lines in the previous window of equal length
	FirstTS    time.Time
	LastTS     time.Time
	// New is true when the template was first seen on the server inside the window.
	New        bool
	Containers []LogDigestContainer // top containers by count
}

// LogDigestTotals are window-wide counts of warning and error lines (before
// the group limit), with the previous window for comparison.
type LogDigestTotals struct {
	Groups     int // templates with at least one line in the window
	Errors     int64
	PrevErrors int64
	Warns      int64
	PrevWarns  int64
}

// LogDigestGroups returns the warning-and-worse (and unknown-level) templates
// seen in [from, to], ranked by severity then count, at most limit, plus the
// window totals. hostID uuid.Nil means every host of the org; containerID ""
// every container. The previous window is [from-(to-from), from).
func (s *Store) LogDigestGroups(ctx context.Context, orgID, hostID uuid.UUID, containerID string, from, to time.Time, limit int) ([]LogDigestGroup, LogDigestTotals, error) {
	var host any
	if hostID != uuid.Nil {
		host = hostID
	}
	prevFrom := from.Add(-(to.Sub(from)))
	var tot LogDigestTotals
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT (e.host_id, e.template_id)) FILTER (WHERE e.last_ts >= $4),
			COALESCE(sum(e.count) FILTER (WHERE e.last_ts >= $4 AND e.level >= 5), 0),
			COALESCE(sum(e.count) FILTER (WHERE e.last_ts < $4 AND e.level >= 5), 0),
			COALESCE(sum(e.count) FILTER (WHERE e.last_ts >= $4 AND e.level < 5), 0),
			COALESCE(sum(e.count) FILTER (WHERE e.last_ts < $4 AND e.level < 5), 0)
		FROM log_events e JOIN hosts h ON h.id = e.host_id AND h.org_id = $1 AND h.revoked_at IS NULL
		WHERE e.org_id = $1 AND ($2::uuid IS NULL OR e.host_id = $2) AND ($3 = '' OR e.container_id = $3)
		  AND e.last_ts >= $5 AND e.last_ts <= $6 AND (e.level >= 4 OR e.level = 0)`,
		orgID, host, containerID, from, prevFrom, to,
	).Scan(&tot.Groups, &tot.Errors, &tot.PrevErrors, &tot.Warns, &tot.PrevWarns)
	if err != nil {
		return nil, LogDigestTotals{}, fmt.Errorf("store: LogDigestGroups totals: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.host_id, h.name, e.template_id, max(e.level), COALESCE(NULLIF(t.template, ''), (array_agg(e.sample ORDER BY e.last_ts DESC))[1]),
			COALESCE(sum(e.count) FILTER (WHERE e.last_ts >= $4), 0)::bigint,
			COALESCE(sum(e.count) FILTER (WHERE e.last_ts < $4), 0)::bigint,
			min(e.first_ts) FILTER (WHERE e.last_ts >= $4), max(e.last_ts) FILTER (WHERE e.last_ts >= $4),
			COALESCE(t.first_seen >= $4, false)
		FROM log_events e
		JOIN hosts h ON h.id = e.host_id AND h.org_id = $1 AND h.revoked_at IS NULL
		LEFT JOIN log_templates t ON t.host_id = e.host_id AND t.template_id = e.template_id
		WHERE e.org_id = $1 AND ($2::uuid IS NULL OR e.host_id = $2) AND ($3 = '' OR e.container_id = $3)
		  AND e.last_ts >= $5 AND e.last_ts <= $6 AND (e.level >= 4 OR e.level = 0)
		GROUP BY e.host_id, h.name, e.template_id, t.template, t.first_seen
		HAVING count(*) FILTER (WHERE e.last_ts >= $4) > 0
		ORDER BY CASE WHEN max(e.level) = 0 THEN 4 ELSE max(e.level) END DESC,
			sum(e.count) FILTER (WHERE e.last_ts >= $4) DESC, e.template_id
		LIMIT $7`,
		orgID, host, containerID, from, prevFrom, to, limit)
	if err != nil {
		return nil, LogDigestTotals{}, fmt.Errorf("store: LogDigestGroups: %w", err)
	}
	defer rows.Close()
	var out []LogDigestGroup
	for rows.Next() {
		var g LogDigestGroup
		if err := rows.Scan(&g.HostID, &g.HostName, &g.TemplateID, &g.Level, &g.Template, &g.Count, &g.PrevCount,
			&g.FirstTS, &g.LastTS, &g.New); err != nil {
			return nil, LogDigestTotals{}, fmt.Errorf("store: LogDigestGroups: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, LogDigestTotals{}, fmt.Errorf("store: LogDigestGroups: %w", err)
	}
	rows.Close()
	if len(out) == 0 {
		return out, tot, nil
	}
	hosts := make([]uuid.UUID, len(out))
	tpls := make([]string, len(out))
	for i, g := range out {
		hosts[i], tpls[i] = g.HostID, g.TemplateID
	}
	crows, err := s.pool.Query(ctx, `
		SELECT e.host_id, e.template_id, e.container_id, COALESCE(max(c.name), ''), sum(e.count)::bigint
		FROM log_events e
		LEFT JOIN containers c ON c.org_id = $1 AND c.host_id = e.host_id AND c.container_id = e.container_id
		WHERE e.org_id = $1 AND e.last_ts >= $2 AND e.last_ts <= $3 AND ($4 = '' OR e.container_id = $4)
		  AND (e.host_id, e.template_id) IN (SELECT * FROM unnest($5::uuid[], $6::text[]))
		GROUP BY e.host_id, e.template_id, e.container_id
		ORDER BY sum(e.count) DESC, e.container_id`,
		orgID, from, to, containerID, hosts, tpls)
	if err != nil {
		return nil, LogDigestTotals{}, fmt.Errorf("store: LogDigestGroups containers: %w", err)
	}
	defer crows.Close()
	idx := map[string]int{}
	for i, g := range out {
		idx[g.HostID.String()+"/"+g.TemplateID] = i
	}
	for crows.Next() {
		var h uuid.UUID
		var tid string
		var c LogDigestContainer
		if err := crows.Scan(&h, &tid, &c.ContainerID, &c.Name, &c.Count); err != nil {
			return nil, LogDigestTotals{}, fmt.Errorf("store: LogDigestGroups containers: %w", err)
		}
		if i, ok := idx[h.String()+"/"+tid]; ok && len(out[i].Containers) < 3 {
			out[i].Containers = append(out[i].Containers, c)
		}
	}
	return out, tot, crows.Err()
}

// ContainerScanHealth is the scan and matching state of one running container.
type ContainerScanHealth struct {
	HostID      uuid.UUID
	HostName    string
	ContainerID string
	Name        string
	Image       string
	ScanStatus  string // pending|ok|failed|timeout|oom|unsupported, or "none" (never scanned)
	ScanError   string
	ImageID     string
	MatchStatus string // ok|failed|pending, or "none" (no match row)
}

// RunningContainerScanHealth returns the latest scan and match state of every
// running container (of one host, or of every host when hostID is uuid.Nil).
func (s *Store) RunningContainerScanHealth(ctx context.Context, orgID, hostID uuid.UUID) ([]ContainerScanHealth, error) {
	var host any
	if hostID != uuid.Nil {
		host = hostID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.host_id, h.name, c.container_id, c.name, c.image,
			COALESCE(ls.status, 'none'), COALESCE(ls.error, ''), COALESCE(ls.image_id, ''), COALESCE(ms.status, 'none')
		FROM containers c
		JOIN hosts h ON h.id = c.host_id AND h.org_id = $1 AND h.revoked_at IS NULL
		LEFT JOIN LATERAL (
			SELECT status, error, image_id FROM scans s
			WHERE s.org_id = $1 AND s.host_id = c.host_id AND s.container_id = c.container_id
			ORDER BY COALESCE(s.finished_at, s.created_at) DESC LIMIT 1
		) ls ON true
		LEFT JOIN image_match_state ms ON ms.org_id = $1 AND ms.image_id = ls.image_id AND ls.image_id <> ''
		WHERE c.org_id = $1 AND c.removed_at IS NULL AND lower(c.state) = 'running'
		  AND ($2::uuid IS NULL OR c.host_id = $2)
		ORDER BY h.name, c.name, c.container_id`, orgID, host)
	if err != nil {
		return nil, fmt.Errorf("store: RunningContainerScanHealth: %w", err)
	}
	defer rows.Close()
	var out []ContainerScanHealth
	for rows.Next() {
		var v ContainerScanHealth
		if err := rows.Scan(&v.HostID, &v.HostName, &v.ContainerID, &v.Name, &v.Image,
			&v.ScanStatus, &v.ScanError, &v.ImageID, &v.MatchStatus); err != nil {
			return nil, fmt.Errorf("store: RunningContainerScanHealth: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// HostMisconfig is an open misconfiguration with its host's name.
type HostMisconfig struct {
	Misconfig
	HostName string
}

// OpenMisconfigsForOrg returns open misconfigurations (of one host, or of
// every host when hostID is uuid.Nil), worst first, at most limit.
func (s *Store) OpenMisconfigsForOrg(ctx context.Context, orgID, hostID uuid.UUID, limit int) ([]HostMisconfig, error) {
	var host any
	if hostID != uuid.Nil {
		host = hostID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.org_id, m.host_id, m.container_id, m.kind, m.severity, m.detail, m.first_seen, m.last_seen, m.resolved_at, h.name
		FROM misconfigs m JOIN hosts h ON h.id = m.host_id AND h.org_id = $1 AND h.revoked_at IS NULL
		WHERE m.org_id = $1 AND m.resolved_at IS NULL AND ($2::uuid IS NULL OR m.host_id = $2)
		ORDER BY `+severityRank("m.severity")+`, m.first_seen, m.kind, m.container_id
		LIMIT $3`, orgID, host, limit)
	if err != nil {
		return nil, fmt.Errorf("store: OpenMisconfigsForOrg: %w", err)
	}
	defer rows.Close()
	var out []HostMisconfig
	for rows.Next() {
		var m HostMisconfig
		if err := rows.Scan(&m.OrgID, &m.HostID, &m.ContainerID, &m.Kind, &m.Severity, &m.Detail,
			&m.FirstSeen, &m.LastSeen, &m.ResolvedAt, &m.HostName); err != nil {
			return nil, fmt.Errorf("store: OpenMisconfigsForOrg: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetSecurityDigestWatermark returns when the last scheduled security digest
// looked at findings; false when there was none yet.
func (s *Store) GetSecurityDigestWatermark(ctx context.Context, orgID uuid.UUID) (time.Time, bool, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx, `SELECT watermark FROM security_digest_state WHERE org_id = $1`, orgID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: GetSecurityDigestWatermark: %w", err)
	}
	return t.UTC(), true, nil
}

// SetSecurityDigestWatermark moves the watermark (never backwards).
func (s *Store) SetSecurityDigestWatermark(ctx context.Context, orgID uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO security_digest_state (org_id, watermark) VALUES ($1, $2)
		ON CONFLICT (org_id) DO UPDATE SET
			watermark = GREATEST(security_digest_state.watermark, EXCLUDED.watermark), updated_at = now()`,
		orgID, at)
	if err != nil {
		return fmt.Errorf("store: SetSecurityDigestWatermark: %w", err)
	}
	return nil
}

// DigestOrg is an organisation with its digest schedule.
type DigestOrg struct {
	OrgID        uuid.UUID
	TimeZone     string
	BriefingHour int
	Enabled      bool // org_settings.ai_digest_enabled
}

// DigestOrgs returns every organisation with its time zone, briefing hour and
// whether the daily digest is enabled (missing settings: UTC, 8, enabled).
func (s *Store) DigestOrgs(ctx context.Context) ([]DigestOrg, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, COALESCE(s.time_zone, 'UTC'), COALESCE(s.briefing_hour, 8), COALESCE(s.ai_digest_enabled, true)
		FROM orgs o LEFT JOIN org_settings s ON s.org_id = o.id
		ORDER BY o.created_at ASC, o.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: DigestOrgs: %w", err)
	}
	defer rows.Close()
	var out []DigestOrg
	for rows.Next() {
		var o DigestOrg
		if err := rows.Scan(&o.OrgID, &o.TimeZone, &o.BriefingHour, &o.Enabled); err != nil {
			return nil, fmt.Errorf("store: DigestOrgs: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DigestTryClaim inserts the (org, local date) run with an exclusive lease and
// reports whether this caller won. localDate's UTC calendar day is stored.
func (s *Store) DigestTryClaim(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO digest_runs (org_id, local_date, lease_until)
		VALUES ($1, $2::date, now() + ($3 * interval '1 second'))
		ON CONFLICT DO NOTHING`,
		orgID, localDate.UTC().Format("2006-01-02"), BriefingLeaseTTL.Seconds())
	if err != nil {
		return false, fmt.Errorf("store: DigestTryClaim: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DigestReclaim takes the lease of an unfinished run whose lease expired (the
// previous owner crashed). It reports whether this caller won.
func (s *Store) DigestReclaim(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error) {
	var got uuid.UUID
	err := s.pool.QueryRow(ctx, `
		UPDATE digest_runs SET lease_until = now() + ($3 * interval '1 second')
		WHERE org_id = $1 AND local_date = $2::date AND done_at IS NULL
		  AND (lease_until IS NULL OR lease_until < now())
		RETURNING org_id`,
		orgID, localDate.UTC().Format("2006-01-02"), BriefingLeaseTTL.Seconds()).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: DigestReclaim: %w", err)
	}
	return true, nil
}

// DigestFinish marks the run done; posted says whether a message was enqueued
// (false: a quiet day).
func (s *Store) DigestFinish(ctx context.Context, orgID uuid.UUID, localDate time.Time, posted bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE digest_runs SET done_at = now(), lease_until = NULL, posted = $3
		WHERE org_id = $1 AND local_date = $2::date AND done_at IS NULL`,
		orgID, localDate.UTC().Format("2006-01-02"), posted)
	if err != nil {
		return fmt.Errorf("store: DigestFinish: %w", err)
	}
	return nil
}

// DigestDelete removes an unfinished run so the next tick retries it.
func (s *Store) DigestDelete(ctx context.Context, orgID uuid.UUID, localDate time.Time) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM digest_runs WHERE org_id = $1 AND local_date = $2::date AND done_at IS NULL`,
		orgID, localDate.UTC().Format("2006-01-02"))
	if err != nil {
		return fmt.Errorf("store: DigestDelete: %w", err)
	}
	return nil
}
