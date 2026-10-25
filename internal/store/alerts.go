package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Alert and incident state values (match 0007 check constraints).
const (
	IncidentOpen     = "open"
	IncidentResolved = "resolved"
	AlertFiring      = "firing"
	AlertResolved    = "resolved"
)

// Alert rule names (match 0007 check constraints).
const (
	RuleCrashLoop    = "crash_loop"
	RuleOOMKill      = "oom_kill"
	RuleUnhealthy    = "unhealthy"
	RuleHostOffline  = "host_offline"
	RuleCPUHigh      = "cpu_high"
	RuleMemHigh      = "mem_high"
	RuleDiskHigh     = "disk_high"
	RuleDeployFailed = "deploy_failed"
	RuleNewCVE       = "new_cve"
)

// DefaultOrgSettings returns the migration defaults for a missing org_settings row.
func DefaultOrgSettings(orgID uuid.UUID) OrgSettings {
	return OrgSettings{
		OrgID:         orgID,
		TimeZone:      "UTC",
		BriefingHour:  8,
		CPUPct:        90,
		MemPct:        90,
		DiskPct:       90,
		SustainedS:    300,
		OfflineAfterS: 120,
	}
}

// GetOrgSettings returns settings for orgID, or the migration defaults when no row exists.
// Server-role: pass orgID explicitly.
func (s *Store) GetOrgSettings(ctx context.Context, orgID uuid.UUID) (OrgSettings, error) {
	var o OrgSettings
	err := s.pool.QueryRow(ctx, `
		SELECT org_id, time_zone, briefing_hour, cpu_pct, mem_pct, disk_pct,
			sustained_s, offline_after_s, updated_at
		FROM org_settings WHERE org_id = $1`, orgID).Scan(
		&o.OrgID, &o.TimeZone, &o.BriefingHour, &o.CPUPct, &o.MemPct, &o.DiskPct,
		&o.SustainedS, &o.OfflineAfterS, &o.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DefaultOrgSettings(orgID), nil
		}
		return OrgSettings{}, fmt.Errorf("store: GetOrgSettings: %w", err)
	}
	return o, nil
}

// UpsertOrgSettings inserts or replaces organisation alert/briefing settings.
func (s *Store) UpsertOrgSettings(ctx context.Context, o OrgSettings) (OrgSettings, error) {
	if o.TimeZone == "" {
		o.TimeZone = "UTC"
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO org_settings (
			org_id, time_zone, briefing_hour, cpu_pct, mem_pct, disk_pct,
			sustained_s, offline_after_s, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (org_id) DO UPDATE SET
			time_zone = EXCLUDED.time_zone,
			briefing_hour = EXCLUDED.briefing_hour,
			cpu_pct = EXCLUDED.cpu_pct,
			mem_pct = EXCLUDED.mem_pct,
			disk_pct = EXCLUDED.disk_pct,
			sustained_s = EXCLUDED.sustained_s,
			offline_after_s = EXCLUDED.offline_after_s,
			updated_at = now()
		RETURNING org_id, time_zone, briefing_hour, cpu_pct, mem_pct, disk_pct,
			sustained_s, offline_after_s, updated_at`,
		o.OrgID, o.TimeZone, o.BriefingHour, o.CPUPct, o.MemPct, o.DiskPct,
		o.SustainedS, o.OfflineAfterS,
	).Scan(
		&o.OrgID, &o.TimeZone, &o.BriefingHour, &o.CPUPct, &o.MemPct, &o.DiskPct,
		&o.SustainedS, &o.OfflineAfterS, &o.UpdatedAt,
	)
	if err != nil {
		return OrgSettings{}, fmt.Errorf("store: UpsertOrgSettings: %w", err)
	}
	return o, nil
}

// ListOrgIDs returns every organisation id (server-role background loops).
func (s *Store) ListOrgIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM orgs ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: ListOrgIDs: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: ListOrgIDs: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const incidentCols = `
	id, org_id, host_id, title, severity, state, opened_at, updated_at, resolved_at`

func scanIncident(row scannable) (Incident, error) {
	var i Incident
	err := row.Scan(
		&i.ID, &i.OrgID, &i.HostID, &i.Title, &i.Severity, &i.State,
		&i.OpenedAt, &i.UpdatedAt, &i.ResolvedAt,
	)
	return i, err
}

const alertCols = `
	id, org_id, host_id, incident_id, rule, container, severity, state,
	dedupe_key, summary, evidence, first_at, last_at, resolved_at`

func scanAlert(row scannable) (Alert, error) {
	var a Alert
	err := row.Scan(
		&a.ID, &a.OrgID, &a.HostID, &a.IncidentID, &a.Rule, &a.Container,
		&a.Severity, &a.State, &a.DedupeKey, &a.Summary, &a.Evidence,
		&a.FirstAt, &a.LastAt, &a.ResolvedAt,
	)
	if err != nil {
		return Alert{}, err
	}
	if len(a.Evidence) == 0 {
		a.Evidence = json.RawMessage(`{}`)
	}
	return a, nil
}

// OpenIncidentForHost returns the open incident for hostID, creating one when none exists.
// Concurrent callers are safe via incidents_one_open: a conflict re-reads the winner.
func (s *Store) OpenIncidentForHost(ctx context.Context, orgID, hostID uuid.UUID, title, severity string) (Incident, bool, error) {
	if title == "" {
		title = "incident"
	}
	if severity == "" {
		severity = SeverityMedium
	}
	inc, err := s.openIncident(ctx, orgID, hostID)
	if err == nil {
		return inc, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Incident{}, false, err
	}

	id, err := uuid.NewRandom()
	if err != nil {
		return Incident{}, false, fmt.Errorf("store: OpenIncidentForHost: %w", err)
	}
	inc, err = scanIncident(s.pool.QueryRow(ctx, `
		INSERT INTO incidents (id, org_id, host_id, title, severity, state)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+incidentCols,
		id, orgID, hostID, title, severity, IncidentOpen,
	))
	if err == nil {
		return inc, true, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		inc, err = s.openIncident(ctx, orgID, hostID)
		if err != nil {
			return Incident{}, false, fmt.Errorf("store: OpenIncidentForHost: %w", err)
		}
		return inc, false, nil
	}
	return Incident{}, false, fmt.Errorf("store: OpenIncidentForHost: %w", err)
}

func (s *Store) openIncident(ctx context.Context, orgID, hostID uuid.UUID) (Incident, error) {
	inc, err := scanIncident(s.pool.QueryRow(ctx, `
		SELECT `+incidentCols+` FROM incidents
		WHERE org_id = $1 AND host_id = $2 AND state = $3`,
		orgID, hostID, IncidentOpen,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Incident{}, fmt.Errorf("store: openIncident: %w", ErrNotFound)
		}
		return Incident{}, fmt.Errorf("store: openIncident: %w", err)
	}
	return inc, nil
}

// UpdateIncident sets title and severity on an open incident and bumps updated_at.
func (s *Store) UpdateIncident(ctx context.Context, orgID, incidentID uuid.UUID, title, severity string) (Incident, error) {
	inc, err := scanIncident(s.pool.QueryRow(ctx, `
		UPDATE incidents SET title = $3, severity = $4, updated_at = now()
		WHERE org_id = $1 AND id = $2 AND state = $5
		RETURNING `+incidentCols,
		orgID, incidentID, title, severity, IncidentOpen,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Incident{}, fmt.Errorf("store: UpdateIncident: %w", ErrNotFound)
		}
		return Incident{}, fmt.Errorf("store: UpdateIncident: %w", err)
	}
	return inc, nil
}

// ResolveIncident marks an open incident resolved.
func (s *Store) ResolveIncident(ctx context.Context, orgID, incidentID uuid.UUID) (Incident, error) {
	inc, err := scanIncident(s.pool.QueryRow(ctx, `
		UPDATE incidents SET state = $3, resolved_at = now(), updated_at = now()
		WHERE org_id = $1 AND id = $2 AND state = $4
		RETURNING `+incidentCols,
		orgID, incidentID, IncidentResolved, IncidentOpen,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Incident{}, fmt.Errorf("store: ResolveIncident: %w", ErrNotFound)
		}
		return Incident{}, fmt.Errorf("store: ResolveIncident: %w", err)
	}
	return inc, nil
}

// UpsertFiringAlert inserts a firing alert or refreshes last_at on the existing
// firing row with the same (org_id, dedupe_key). Idempotent under alerts_one_firing.
func (s *Store) UpsertFiringAlert(ctx context.Context, a Alert) (Alert, bool, error) {
	if a.Evidence == nil {
		a.Evidence = json.RawMessage(`{}`)
	}
	if a.ID == uuid.Nil {
		id, err := uuid.NewRandom()
		if err != nil {
			return Alert{}, false, fmt.Errorf("store: UpsertFiringAlert: %w", err)
		}
		a.ID = id
	}
	var created bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO alerts (
			id, org_id, host_id, incident_id, rule, container, severity, state,
			dedupe_key, summary, evidence, first_at, last_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now(),now()
		)
		ON CONFLICT (org_id, dedupe_key) WHERE (state = 'firing') DO UPDATE SET
			last_at = now(),
			summary = EXCLUDED.summary,
			evidence = EXCLUDED.evidence,
			severity = EXCLUDED.severity,
			incident_id = EXCLUDED.incident_id,
			container = EXCLUDED.container
		RETURNING `+alertCols+`, (xmax = 0)`,
		a.ID, a.OrgID, a.HostID, a.IncidentID, a.Rule, a.Container, a.Severity, AlertFiring,
		a.DedupeKey, a.Summary, a.Evidence,
	).Scan(
		&a.ID, &a.OrgID, &a.HostID, &a.IncidentID, &a.Rule, &a.Container,
		&a.Severity, &a.State, &a.DedupeKey, &a.Summary, &a.Evidence,
		&a.FirstAt, &a.LastAt, &a.ResolvedAt, &created,
	)
	if err != nil {
		return Alert{}, false, fmt.Errorf("store: UpsertFiringAlert: %w", err)
	}
	if len(a.Evidence) == 0 {
		a.Evidence = json.RawMessage(`{}`)
	}
	return a, created, nil
}

// ResolveAlert marks a firing alert resolved by org and dedupe key.
func (s *Store) ResolveAlert(ctx context.Context, orgID uuid.UUID, dedupeKey string) (Alert, error) {
	a, err := scanAlert(s.pool.QueryRow(ctx, `
		UPDATE alerts SET state = $3, resolved_at = now(), last_at = now()
		WHERE org_id = $1 AND dedupe_key = $2 AND state = $4
		RETURNING `+alertCols,
		orgID, dedupeKey, AlertResolved, AlertFiring,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Alert{}, fmt.Errorf("store: ResolveAlert: %w", ErrNotFound)
		}
		return Alert{}, fmt.Errorf("store: ResolveAlert: %w", err)
	}
	return a, nil
}

// ListFiringAlerts returns every firing alert for orgID.
func (s *Store) ListFiringAlerts(ctx context.Context, orgID uuid.UUID) ([]Alert, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+alertCols+` FROM alerts
		WHERE org_id = $1 AND state = $2
		ORDER BY last_at DESC, id`,
		orgID, AlertFiring,
	)
	if err != nil {
		return nil, fmt.Errorf("store: ListFiringAlerts: %w", err)
	}
	defer rows.Close()
	return collectAlerts(rows)
}

// ListIncidents returns incidents for the organisation, newest first.
// state empty means all states. limit is capped at 200 (default 50).
// Intended for ForOrg (request-scoped) or server role with explicit orgID.
func (s *Store) ListIncidents(ctx context.Context, orgID uuid.UUID, state string, limit int) ([]Incident, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		rows pgx.Rows
		err  error
	)
	if state == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT `+incidentCols+` FROM incidents
			WHERE org_id = $1
			ORDER BY updated_at DESC, id DESC
			LIMIT $2`, orgID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+incidentCols+` FROM incidents
			WHERE org_id = $1 AND state = $2
			ORDER BY updated_at DESC, id DESC
			LIMIT $3`, orgID, state, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("store: ListIncidents: %w", err)
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ListIncidents: %w", err)
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// GetIncident returns one incident and its alerts (firing first, then by last_at).
func (s *Store) GetIncident(ctx context.Context, orgID, id uuid.UUID) (Incident, []Alert, error) {
	inc, err := scanIncident(s.pool.QueryRow(ctx, `
		SELECT `+incidentCols+` FROM incidents WHERE org_id = $1 AND id = $2`, orgID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Incident{}, nil, fmt.Errorf("store: GetIncident: %w", ErrNotFound)
		}
		return Incident{}, nil, fmt.Errorf("store: GetIncident: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+alertCols+` FROM alerts
		WHERE org_id = $1 AND incident_id = $2
		ORDER BY CASE state WHEN 'firing' THEN 0 ELSE 1 END, last_at DESC, id`,
		orgID, id,
	)
	if err != nil {
		return Incident{}, nil, fmt.Errorf("store: GetIncident: %w", err)
	}
	defer rows.Close()
	alerts, err := collectAlerts(rows)
	if err != nil {
		return Incident{}, nil, fmt.Errorf("store: GetIncident: %w", err)
	}
	return inc, alerts, nil
}

// ListAlerts returns alerts for orgID, optionally filtered by state. limit capped at 200.
func (s *Store) ListAlerts(ctx context.Context, orgID uuid.UUID, state string, limit int) ([]Alert, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		rows pgx.Rows
		err  error
	)
	if state == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT `+alertCols+` FROM alerts
			WHERE org_id = $1
			ORDER BY last_at DESC, id DESC
			LIMIT $2`, orgID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+alertCols+` FROM alerts
			WHERE org_id = $1 AND state = $2
			ORDER BY last_at DESC, id DESC
			LIMIT $3`, orgID, state, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("store: ListAlerts: %w", err)
	}
	defer rows.Close()
	return collectAlerts(rows)
}

func collectAlerts(rows pgx.Rows) ([]Alert, error) {
	var out []Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const muteCols = `id, org_id, host_id, rule, until, created_by, created_at`

func scanMute(row scannable) (Mute, error) {
	var m Mute
	err := row.Scan(&m.ID, &m.OrgID, &m.HostID, &m.Rule, &m.Until, &m.CreatedBy, &m.CreatedAt)
	return m, err
}

// CreateMute inserts a mute. Prefer ForOrg for request-scoped writes.
func (s *Store) CreateMute(ctx context.Context, m Mute) (Mute, error) {
	if m.ID == uuid.Nil {
		id, err := uuid.NewRandom()
		if err != nil {
			return Mute{}, fmt.Errorf("store: CreateMute: %w", err)
		}
		m.ID = id
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mutes (id, org_id, host_id, rule, until, created_by)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+muteCols,
		m.ID, m.OrgID, m.HostID, m.Rule, m.Until, m.CreatedBy,
	).Scan(&m.ID, &m.OrgID, &m.HostID, &m.Rule, &m.Until, &m.CreatedBy, &m.CreatedAt)
	if err != nil {
		return Mute{}, fmt.Errorf("store: CreateMute: %w", err)
	}
	return m, nil
}

// UpdateMuteUntil extends or shortens a mute. Prefer ForOrg for request-scoped writes.
func (s *Store) UpdateMuteUntil(ctx context.Context, orgID, id uuid.UUID, until time.Time) (Mute, error) {
	m, err := scanMute(s.pool.QueryRow(ctx, `
		UPDATE mutes SET until = $3 WHERE org_id = $1 AND id = $2
		RETURNING `+muteCols, orgID, id, until))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Mute{}, fmt.Errorf("store: UpdateMuteUntil: %w", ErrNotFound)
		}
		return Mute{}, fmt.Errorf("store: UpdateMuteUntil: %w", err)
	}
	return m, nil
}

// DeleteMute removes a mute. Prefer ForOrg for request-scoped writes.
func (s *Store) DeleteMute(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mutes WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("store: DeleteMute: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: DeleteMute: %w", ErrNotFound)
	}
	return nil
}

// ListMutes returns mutes for orgID ordered by until descending.
func (s *Store) ListMutes(ctx context.Context, orgID uuid.UUID) ([]Mute, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+muteCols+` FROM mutes WHERE org_id = $1 ORDER BY until DESC, id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: ListMutes: %w", err)
	}
	defer rows.Close()
	var out []Mute
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ListMutes: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ActiveMutes returns mutes still in force at now (until > now).
func (s *Store) ActiveMutes(ctx context.Context, orgID uuid.UUID, now time.Time) ([]Mute, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+muteCols+` FROM mutes
		WHERE org_id = $1 AND until > $2
		ORDER BY until DESC, id`, orgID, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("store: ActiveMutes: %w", err)
	}
	defer rows.Close()
	var out []Mute
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ActiveMutes: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MuteMatches reports whether m suppresses notifications for hostID and rule.
// A nil host matches all hosts; an empty rule matches all rules.
func MuteMatches(m Mute, hostID uuid.UUID, rule string) bool {
	if m.HostID != nil && *m.HostID != hostID {
		return false
	}
	if m.Rule != "" && m.Rule != rule {
		return false
	}
	return true
}

// ContainerRestartFact is restart_count for one live container (rule engine input).
type ContainerRestartFact struct {
	HostID       uuid.UUID
	ContainerID  string
	Name         string
	RestartCount int
	UpdatedAt    time.Time
	StartedAt    *time.Time
}

// ContainerRestarts returns live containers and their restart counts for orgID.
func (s *Store) ContainerRestarts(ctx context.Context, orgID uuid.UUID) ([]ContainerRestartFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host_id, container_id, name, restart_count, updated_at, started_at
		FROM containers
		WHERE org_id = $1 AND removed_at IS NULL
		ORDER BY host_id, name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: ContainerRestarts: %w", err)
	}
	defer rows.Close()
	var out []ContainerRestartFact
	for rows.Next() {
		var f ContainerRestartFact
		if err := rows.Scan(&f.HostID, &f.ContainerID, &f.Name, &f.RestartCount, &f.UpdatedAt, &f.StartedAt); err != nil {
			return nil, fmt.Errorf("store: ContainerRestarts: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ContainerOOMFact is a container reported OOM-killed within a window.
type ContainerOOMFact struct {
	HostID      uuid.UUID
	ContainerID string
	Name        string
	UpdatedAt   time.Time
}

// ContainersOOMSince returns live containers with oom_killed whose row was
// updated at or after since (proxy for "OOM in the last window").
func (s *Store) ContainersOOMSince(ctx context.Context, orgID uuid.UUID, since time.Time) ([]ContainerOOMFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host_id, container_id, name, updated_at
		FROM containers
		WHERE org_id = $1 AND removed_at IS NULL AND oom_killed = true AND updated_at >= $2
		ORDER BY host_id, name`, orgID, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("store: ContainersOOMSince: %w", err)
	}
	defer rows.Close()
	var out []ContainerOOMFact
	for rows.Next() {
		var f ContainerOOMFact
		if err := rows.Scan(&f.HostID, &f.ContainerID, &f.Name, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: ContainersOOMSince: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ContainerHealthFact is current health for one live container.
type ContainerHealthFact struct {
	HostID      uuid.UUID
	ContainerID string
	Name        string
	Health      string
	UpdatedAt   time.Time
}

// ContainerHealth returns live containers' health fields for orgID.
func (s *Store) ContainerHealth(ctx context.Context, orgID uuid.UUID) ([]ContainerHealthFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host_id, container_id, name, health, updated_at
		FROM containers
		WHERE org_id = $1 AND removed_at IS NULL
		ORDER BY host_id, name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: ContainerHealth: %w", err)
	}
	defer rows.Close()
	var out []ContainerHealthFact
	for rows.Next() {
		var f ContainerHealthFact
		if err := rows.Scan(&f.HostID, &f.ContainerID, &f.Name, &f.Health, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: ContainerHealth: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// HostMetricSustained reports whether every raw host_metrics sample in
// [now-sustained, now] is at or above threshold for the named metric
// ("cpu_pct", "mem_pct", "disk_pct"). Requires at least one sample; an empty
// window is not sustained.
func (s *Store) HostMetricSustained(ctx context.Context, orgID, hostID uuid.UUID, metric string, threshold float64, sustained time.Duration, now time.Time) (bool, error) {
	from := now.UTC().Add(-sustained)
	var q string
	switch metric {
	case "cpu_pct":
		q = `
			SELECT
				COUNT(*) > 0 AND BOOL_AND(cpu_pct >= $4)
			FROM host_metrics
			WHERE org_id = $1 AND host_id = $2 AND ts >= $3 AND ts <= $5`
	case "mem_pct":
		q = `
			SELECT
				COUNT(*) > 0 AND BOOL_AND(
					CASE WHEN mem_total > 0
						THEN ((mem_total - mem_avail)::float8 * 100.0 / mem_total) >= $4
						ELSE false
					END)
			FROM host_metrics
			WHERE org_id = $1 AND host_id = $2 AND ts >= $3 AND ts <= $5`
	case "disk_pct":
		q = `
			SELECT
				COUNT(*) > 0 AND BOOL_AND(
					CASE WHEN disk_total > 0
						THEN (disk_used::float8 * 100.0 / disk_total) >= $4
						ELSE false
					END)
			FROM host_metrics
			WHERE org_id = $1 AND host_id = $2 AND ts >= $3 AND ts <= $5`
	default:
		return false, fmt.Errorf("store: HostMetricSustained: unknown metric %q", metric)
	}
	var ok bool
	err := s.pool.QueryRow(ctx, q, orgID, hostID, from, threshold, now.UTC()).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: HostMetricSustained: %w", err)
	}
	return ok, nil
}

// HostDiskPct returns the latest disk used percentage for a host, or 0 when unknown.
func (s *Store) HostDiskPct(ctx context.Context, orgID, hostID uuid.UUID) (float64, error) {
	var pct float64
	err := s.pool.QueryRow(ctx, `
		SELECT CASE WHEN disk_total > 0
			THEN disk_used::float8 * 100.0 / disk_total
			ELSE 0 END
		FROM host_metrics
		WHERE org_id = $1 AND host_id = $2
		ORDER BY ts DESC
		LIMIT 1`, orgID, hostID).Scan(&pct)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: HostDiskPct: %w", err)
	}
	return pct, nil
}

// HostsOfflineSince returns hosts with no agent session since cutoff:
// last_seen_at IS NULL or last_seen_at < cutoff (and not revoked).
func (s *Store) HostsOfflineSince(ctx context.Context, orgID uuid.UUID, cutoff time.Time) ([]Host, error) {
	rows, err := s.pool.Query(ctx, hostSelect+`
		WHERE org_id = $1 AND revoked_at IS NULL
		  AND (last_seen_at IS NULL OR last_seen_at < $2)
		ORDER BY name`, orgID, cutoff.UTC())
	if err != nil {
		return nil, fmt.Errorf("store: HostsOfflineSince: %w", err)
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, fmt.Errorf("store: HostsOfflineSince: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RecentFailedDeploys returns deploys that finished failed/rolled_back/rollback_failed
// at or after since.
func (s *Store) RecentFailedDeploys(ctx context.Context, orgID uuid.UUID, since time.Time) ([]Deploy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+deployCols+` FROM deploys
		WHERE org_id = $1
		  AND status = ANY($2)
		  AND COALESCE(finished_at, created_at) >= $3
		ORDER BY COALESCE(finished_at, created_at) DESC, id DESC`,
		orgID,
		[]string{DeployFailed, DeployRolledBack, DeployRollbackFailed},
		since.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("store: RecentFailedDeploys: %w", err)
	}
	defer rows.Close()
	return collectDeploys(rows)
}
