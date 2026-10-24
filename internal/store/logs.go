package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Log levels as stored. They match LogLevel in the agent protocol; 0 means
// unknown (the agent could not tell) and is never judged routine.
const (
	LevelTrace int16 = 1
	LevelDebug int16 = 2
	LevelInfo  int16 = 3
	LevelWarn  int16 = 4
	LevelError int16 = 5
	LevelFatal int16 = 6
)

// Template verdicts.
const (
	VerdictNew   = "new"   // not judged yet; agents keep sending it
	VerdictGreen = "green" // routine; agents skip it
	VerdictWatch = "watch" // judged suspicious; always sent
)

// LogEvent is a merged, redacted group of identical log lines.
type LogEvent struct {
	ID          int64
	OrgID       uuid.UUID
	HostID      uuid.UUID
	ContainerID string
	TemplateID  string
	Level       int16
	Template    string // input only: used to create the template row, not stored per event
	Sample      string
	Count       int
	FirstTS     time.Time
	LastTS      time.Time
}

// LogTemplate is a line shape with its judgement.
type LogTemplate struct {
	OrgID       uuid.UUID
	TemplateID  string
	Level       int16
	Template    string
	Sample      string
	FirstSeen   time.Time
	LastSeen    time.Time
	Occurrences int64
	Windows     int
	Verdict     string
}

// LogStats are the cumulative counters an agent reports.
type LogStats struct {
	LinesIn, LinesSkipped, EventsOut, RedactionFailures, LinesDropped int64
}

// InsertLogEvents stores events and updates the template table (occurrences,
// windows, last seen) in one transaction. Unknown templates are created as "new".
func (s *Store) InsertLogEvents(ctx context.Context, orgID, hostID uuid.UUID, evs []LogEvent) error {
	if len(evs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, e := range evs {
		batch.Queue(`
			INSERT INTO log_events (org_id, host_id, container_id, template_id, level, sample, count, first_ts, last_ts)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			orgID, hostID, e.ContainerID, e.TemplateID, e.Level, e.Sample, e.Count, e.FirstTS, e.LastTS)
		batch.Queue(`
			INSERT INTO log_templates (org_id, host_id, template_id, level, template, sample, occurrences, windows, last_seen)
			VALUES ($1,$8,$2,$3,$4,$5,$6,1,$7)
			ON CONFLICT (host_id, template_id) DO UPDATE SET
				occurrences = log_templates.occurrences + EXCLUDED.occurrences,
				windows = log_templates.windows + 1,
				last_seen = GREATEST(log_templates.last_seen, EXCLUDED.last_seen)`,
			orgID, e.TemplateID, e.Level, e.Template, e.Sample, e.Count, e.LastTS, hostID)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: InsertLogEvents: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	br := tx.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("store: InsertLogEvents: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("store: InsertLogEvents: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: InsertLogEvents: %w", err)
	}
	return nil
}

// UpsertTemplateCandidates records templates an agent saw but has not sent
// events for (they may be green elsewhere); existing rows are left alone.
func (s *Store) UpsertTemplateCandidates(ctx context.Context, orgID, hostID uuid.UUID, ts []LogTemplate) error {
	if len(ts) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, t := range ts {
		batch.Queue(`
			INSERT INTO log_templates (org_id, host_id, template_id, level, template, sample)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (host_id, template_id) DO NOTHING`,
			orgID, hostID, t.TemplateID, t.Level, t.Template, t.Sample)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range ts {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: UpsertTemplateCandidates: %w", err)
		}
	}
	return nil
}

// JudgeableTemplates returns unjudged info, debug and trace templates seen at
// least minOccurrences times in at least minWindows windows. Warn, error,
// fatal and unknown-level templates are never returned: they cannot become green.
func (s *Store) JudgeableTemplates(ctx context.Context, orgID, hostID uuid.UUID, minOccurrences int64, minWindows, limit int) ([]LogTemplate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT template_id, level, template, sample, occurrences, windows
		FROM log_templates
		WHERE org_id = $1 AND host_id = $6 AND verdict = 'new' AND level BETWEEN 1 AND $2
		  AND occurrences >= $3 AND windows >= $4
		ORDER BY occurrences DESC LIMIT $5`,
		orgID, LevelInfo, minOccurrences, minWindows, limit, hostID)
	if err != nil {
		return nil, fmt.Errorf("store: JudgeableTemplates: %w", err)
	}
	defer rows.Close()
	var out []LogTemplate
	for rows.Next() {
		t := LogTemplate{OrgID: orgID}
		if err := rows.Scan(&t.TemplateID, &t.Level, &t.Template, &t.Sample, &t.Occurrences, &t.Windows); err != nil {
			return nil, fmt.Errorf("store: JudgeableTemplates: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetVerdicts marks templates green or watch. Only unjudged templates of
// level trace to info change; warn, error, fatal and unknown can never become green.
func (s *Store) SetVerdicts(ctx context.Context, orgID, hostID uuid.UUID, green, watch []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: SetVerdicts: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for verdict, ids := range map[string][]string{VerdictGreen: green, VerdictWatch: watch} {
		if len(ids) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE log_templates SET verdict = $3, judged_at = now()
			WHERE org_id = $1 AND host_id = $5 AND template_id = ANY($2) AND verdict = 'new' AND level BETWEEN 1 AND $4`,
			orgID, ids, verdict, LevelInfo, hostID); err != nil {
			return fmt.Errorf("store: SetVerdicts: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: SetVerdicts: %w", err)
	}
	return nil
}

// GreenTemplateIDs returns the host's green template ids (at most limit, most
// frequent first).
func (s *Store) GreenTemplateIDs(ctx context.Context, orgID, hostID uuid.UUID, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT template_id FROM log_templates
		WHERE org_id = $1 AND host_id = $3 AND verdict = 'green'
		ORDER BY occurrences DESC, template_id LIMIT $2`, orgID, limit, hostID)
	if err != nil {
		return nil, fmt.Errorf("store: GreenTemplateIDs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: GreenTemplateIDs: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListLogEvents returns events of one host in [from, to] at or above minLevel,
// newest first. Scoped by org.
func (s *Store) ListLogEvents(ctx context.Context, orgID, hostID uuid.UUID, from, to time.Time, minLevel int16, limit int) ([]LogEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, container_id, template_id, level, sample, count, first_ts, last_ts
		FROM log_events
		WHERE org_id = $1 AND host_id = $2 AND last_ts >= $3 AND last_ts <= $4 AND level >= $5
		ORDER BY last_ts DESC, id DESC LIMIT $6`,
		orgID, hostID, from, to, minLevel, limit)
	if err != nil {
		return nil, fmt.Errorf("store: ListLogEvents: %w", err)
	}
	defer rows.Close()
	var out []LogEvent
	for rows.Next() {
		e := LogEvent{OrgID: orgID, HostID: hostID}
		if err := rows.Scan(&e.ID, &e.ContainerID, &e.TemplateID, &e.Level, &e.Sample, &e.Count, &e.FirstTS, &e.LastTS); err != nil {
			return nil, fmt.Errorf("store: ListLogEvents: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetLogStats stores an agent's cumulative counters.
func (s *Store) SetLogStats(ctx context.Context, orgID, hostID uuid.UUID, st LogStats) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO log_stats (host_id, org_id, lines_in, lines_skipped, events_out, redaction_failures, lines_dropped, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (host_id) DO UPDATE SET
			lines_in = EXCLUDED.lines_in, lines_skipped = EXCLUDED.lines_skipped, events_out = EXCLUDED.events_out,
			redaction_failures = EXCLUDED.redaction_failures, lines_dropped = EXCLUDED.lines_dropped, updated_at = now()`,
		hostID, orgID, st.LinesIn, st.LinesSkipped, st.EventsOut, st.RedactionFailures, st.LinesDropped)
	if err != nil {
		return fmt.Errorf("store: SetLogStats: %w", err)
	}
	return nil
}

// GetLogStats returns an agent's last reported counters, scoped by org.
func (s *Store) GetLogStats(ctx context.Context, orgID, hostID uuid.UUID) (LogStats, error) {
	var st LogStats
	err := s.pool.QueryRow(ctx, `
		SELECT lines_in, lines_skipped, events_out, redaction_failures, lines_dropped
		FROM log_stats WHERE org_id = $1 AND host_id = $2`, orgID, hostID,
	).Scan(&st.LinesIn, &st.LinesSkipped, &st.EventsOut, &st.RedactionFailures, &st.LinesDropped)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LogStats{}, fmt.Errorf("store: GetLogStats: %w", ErrNotFound)
		}
		return LogStats{}, fmt.Errorf("store: GetLogStats: %w", err)
	}
	return st, nil
}

// PruneLogs deletes events older than eventRetention and templates unseen for
// templateRetention.
func (s *Store) PruneLogs(ctx context.Context, now time.Time, eventRetention, templateRetention time.Duration) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM log_events WHERE last_ts < $1`, now.Add(-eventRetention)); err != nil {
		return fmt.Errorf("store: PruneLogs: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM log_templates WHERE last_seen < $1`, now.Add(-templateRetention)); err != nil {
		return fmt.Errorf("store: PruneLogs: %w", err)
	}
	return nil
}
