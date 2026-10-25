package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// BriefingOrg is an organisation with its briefing schedule settings.
type BriefingOrg struct {
	OrgID        uuid.UUID
	TimeZone     string
	BriefingHour int
}

// BriefingItem is one open incident or firing alert for the daily briefing.
type BriefingItem struct {
	Severity string
	Title    string
	HostName string
	Kind     string // incident | alert
}

// BriefingFacts is the snapshot Compose turns into briefing text.
type BriefingFacts struct {
	Hosts      int
	Online     int
	Containers map[string]int // Docker state → count (current, not removed)
	Items      []BriefingItem // open incidents and firing alerts since the cutoff
}

// BriefingOrgs returns every organisation with its time zone and briefing hour.
// Missing org_settings rows use the migration defaults (UTC, hour 8).
func (s *Store) BriefingOrgs(ctx context.Context) ([]BriefingOrg, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id,
			COALESCE(s.time_zone, 'UTC'),
			COALESCE(s.briefing_hour, 8)
		FROM orgs o
		LEFT JOIN org_settings s ON s.org_id = o.id
		ORDER BY o.created_at ASC, o.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: BriefingOrgs: %w", err)
	}
	defer rows.Close()

	var out []BriefingOrg
	for rows.Next() {
		var o BriefingOrg
		if err := rows.Scan(&o.OrgID, &o.TimeZone, &o.BriefingHour); err != nil {
			return nil, fmt.Errorf("store: BriefingOrgs: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: BriefingOrgs: %w", err)
	}
	return out, nil
}

// BriefingFacts returns host/container counts and open incidents plus firing
// alerts with last_at >= since for orgID. Host names come from the hosts table.
func (s *Store) BriefingFacts(ctx context.Context, orgID uuid.UUID, since time.Time) (BriefingFacts, error) {
	var f BriefingFacts
	f.Containers = map[string]int{}

	err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE connected)
		FROM hosts
		WHERE org_id = $1 AND revoked_at IS NULL`,
		orgID,
	).Scan(&f.Hosts, &f.Online)
	if err != nil {
		return BriefingFacts{}, fmt.Errorf("store: BriefingFacts hosts: %w", err)
	}

	crows, err := s.pool.Query(ctx, `
		SELECT state, count(*)
		FROM containers
		WHERE org_id = $1 AND removed_at IS NULL
		GROUP BY state`,
		orgID,
	)
	if err != nil {
		return BriefingFacts{}, fmt.Errorf("store: BriefingFacts containers: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var state string
		var n int
		if err := crows.Scan(&state, &n); err != nil {
			return BriefingFacts{}, fmt.Errorf("store: BriefingFacts containers: %w", err)
		}
		f.Containers[state] = n
	}
	if err := crows.Err(); err != nil {
		return BriefingFacts{}, fmt.Errorf("store: BriefingFacts containers: %w", err)
	}

	// Open incidents are always included; resolved ones only if updated within
	// the 24h window (since).
	irows, err := s.pool.Query(ctx, `
		SELECT i.severity, i.title, h.name
		FROM incidents i
		JOIN hosts h ON h.id = i.host_id
		WHERE i.org_id = $1 AND (
			i.state = 'open'
			OR (i.state = 'resolved' AND i.updated_at >= $2)
		)
		ORDER BY
			CASE i.severity
				WHEN 'critical' THEN 4
				WHEN 'high' THEN 3
				WHEN 'medium' THEN 2
				WHEN 'low' THEN 1
				ELSE 0
			END DESC,
			i.opened_at DESC`,
		orgID, since,
	)
	if err != nil {
		return BriefingFacts{}, fmt.Errorf("store: BriefingFacts incidents: %w", err)
	}
	defer irows.Close()
	for irows.Next() {
		var it BriefingItem
		if err := irows.Scan(&it.Severity, &it.Title, &it.HostName); err != nil {
			return BriefingFacts{}, fmt.Errorf("store: BriefingFacts incidents: %w", err)
		}
		it.Kind = "incident"
		f.Items = append(f.Items, it)
	}
	if err := irows.Err(); err != nil {
		return BriefingFacts{}, fmt.Errorf("store: BriefingFacts incidents: %w", err)
	}

	arows, err := s.pool.Query(ctx, `
		SELECT a.severity, a.summary, h.name
		FROM alerts a
		JOIN hosts h ON h.id = a.host_id
		WHERE a.org_id = $1 AND a.state = 'firing' AND a.last_at >= $2
		ORDER BY
			CASE a.severity
				WHEN 'critical' THEN 4
				WHEN 'high' THEN 3
				WHEN 'medium' THEN 2
				WHEN 'low' THEN 1
				ELSE 0
			END DESC,
			a.last_at DESC`,
		orgID, since,
	)
	if err != nil {
		return BriefingFacts{}, fmt.Errorf("store: BriefingFacts alerts: %w", err)
	}
	defer arows.Close()
	for arows.Next() {
		var it BriefingItem
		if err := arows.Scan(&it.Severity, &it.Title, &it.HostName); err != nil {
			return BriefingFacts{}, fmt.Errorf("store: BriefingFacts alerts: %w", err)
		}
		it.Kind = "alert"
		f.Items = append(f.Items, it)
	}
	if err := arows.Err(); err != nil {
		return BriefingFacts{}, fmt.Errorf("store: BriefingFacts alerts: %w", err)
	}

	return f, nil
}

// BriefingTryClaim inserts (org_id, local_date) into briefings. It returns true
// when this caller won the insert (ON CONFLICT DO NOTHING). localDate's
// calendar day in UTC is stored as the PostgreSQL date.
func (s *Store) BriefingTryClaim(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO briefings (org_id, local_date)
		VALUES ($1, $2::date)
		ON CONFLICT DO NOTHING`,
		orgID, localDate.UTC().Format("2006-01-02"),
	)
	if err != nil {
		return false, fmt.Errorf("store: BriefingTryClaim: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// BriefingReclaimUnenqueued returns true when a claim exists with enqueued_at
// still NULL (crash between claim and enqueue), so the scheduler should finish it.
func (s *Store) BriefingReclaimUnenqueued(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE briefings SET created_at = created_at
		WHERE org_id = $1 AND local_date = $2::date AND enqueued_at IS NULL`,
		orgID, localDate.UTC().Format("2006-01-02"),
	)
	if err != nil {
		return false, fmt.Errorf("store: BriefingReclaimUnenqueued: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// BriefingMarkEnqueued records that the briefing message was enqueued.
func (s *Store) BriefingMarkEnqueued(ctx context.Context, orgID uuid.UUID, localDate time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE briefings SET enqueued_at = now()
		WHERE org_id = $1 AND local_date = $2::date AND enqueued_at IS NULL`,
		orgID, localDate.UTC().Format("2006-01-02"),
	)
	if err != nil {
		return fmt.Errorf("store: BriefingMarkEnqueued: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Already marked or missing — not an error for idempotent callers.
		return nil
	}
	return nil
}

// BriefingDelete removes a claim so a failed enqueue can be retried.
func (s *Store) BriefingDelete(ctx context.Context, orgID uuid.UUID, localDate time.Time) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM briefings
		WHERE org_id = $1 AND local_date = $2::date`,
		orgID, localDate.UTC().Format("2006-01-02"),
	)
	if err != nil {
		return fmt.Errorf("store: BriefingDelete: %w", err)
	}
	return nil
}
