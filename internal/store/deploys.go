package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alexseymer/haetae/internal/deployphase"
)

// Deploy field and log size limits.
const (
	MaxDeployTextBytes = 8 << 10  // commit/url/error/message
	MaxLogChunkBytes   = 64 << 10 // one deploy_logs.text
	MaxDeployLogBytes  = 2 << 20  // total text stored per deploy
	DeployLogTruncMsg  = "[log truncated]"
)

// Deploy statuses (must match deployphase and the deploys.status check).
const (
	DeployQueued         = deployphase.Queued
	DeployFetching       = deployphase.Fetching
	DeployBuilding       = deployphase.Building
	DeployStarting       = deployphase.Starting
	DeployChecking       = deployphase.Checking
	DeployHealthy        = deployphase.Healthy
	DeployFailed         = deployphase.Failed
	DeployRollingBack    = deployphase.RollingBack
	DeployRolledBack     = deployphase.RolledBack
	DeployRollbackFailed = deployphase.RollbackFailed
)

// ErrActiveDeploy is returned when CreateDeploy hits the one-active-per-project index.
var ErrActiveDeploy = errors.New("store: project already has an active deploy")

// Deploy triggers.
const (
	TriggerManual   = "manual"
	TriggerPush     = "push"
	TriggerRollback = "rollback"
)

// Deploy is one attempt to make a project run a version.
type Deploy struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	HostID        uuid.UUID
	ProjectID     uuid.UUID
	Status        string
	Commit        string
	SourceKind    string
	GitURL        string
	GitRef        string
	ComposePath   string
	InlineCompose string
	Trigger       string
	RollbackOf    *uuid.UUID
	StartedBy     *uuid.UUID
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	URL           string
	Error         string
	Message       string
	LastEventAt   *time.Time
}

// DeployEvent is a phase change reported by an agent.
type DeployEvent struct {
	Phase   string
	Message string
	Commit  string
	URL     string
	Error   string
}

// DeployLogChunk is one redacted log line group from an agent.
type DeployLogChunk struct {
	Seq      int
	Stream   string
	Text     string
	Withheld bool
}

// DeployLog is a stored log chunk.
type DeployLog struct {
	DeployID uuid.UUID
	Seq      int
	Stream   string
	Text     string
	Withheld bool
}

var runningStatuses = []string{
	DeployQueued, DeployFetching, DeployBuilding,
	DeployStarting, DeployChecking, DeployRollingBack,
}

func isFinalStatus(s string) bool {
	return deployphase.IsFinal(s)
}

func clipDeployText(s string) string {
	if len(s) > MaxDeployTextBytes {
		s = s[:MaxDeployTextBytes]
	}
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	// Drop NULs; PostgreSQL text rejects them.
	if strings.ContainsRune(s, 0) {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if !utf8.ValidString(s) {
		return ""
	}
	return s
}

func clipLogText(s string) string {
	if len(s) > MaxLogChunkBytes {
		s = s[:MaxLogChunkBytes]
	}
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	if strings.ContainsRune(s, 0) {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if !utf8.ValidString(s) {
		return ""
	}
	return s
}

// phaseAllowed reports whether moving from cur to next is a valid forward step.
// The table lives in deployphase so the agent and store stay aligned.
func phaseAllowed(cur, next string) bool {
	return deployphase.Allowed(cur, next)
}

const deployCols = `
	id, org_id, host_id, project_id, status, commit, source_kind, git_url, git_ref,
	compose_path, inline_compose, trigger, rollback_of, started_by,
	created_at, started_at, finished_at, url, error, message, last_event_at`

func scanDeploy(row scannable) (Deploy, error) {
	var d Deploy
	err := row.Scan(
		&d.ID, &d.OrgID, &d.HostID, &d.ProjectID, &d.Status, &d.Commit, &d.SourceKind,
		&d.GitURL, &d.GitRef, &d.ComposePath, &d.InlineCompose, &d.Trigger,
		&d.RollbackOf, &d.StartedBy, &d.CreatedAt, &d.StartedAt, &d.FinishedAt,
		&d.URL, &d.Error, &d.Message, &d.LastEventAt,
	)
	return d, err
}

// CreateDeploy inserts a deploy that snapshots the project's source. The git
// token is not copied. status starts as queued.
func (s *Store) CreateDeploy(ctx context.Context, p Project, trigger string, rollbackOf, startedBy *uuid.UUID) (Deploy, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return Deploy{}, fmt.Errorf("store: CreateDeploy: %w", err)
	}
	d, err := scanDeploy(s.pool.QueryRow(ctx, `
		INSERT INTO deploys (
			id, org_id, host_id, project_id, status, source_kind, git_url, git_ref,
			compose_path, inline_compose, trigger, rollback_of, started_by, last_event_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now()
		) RETURNING `+deployCols,
		id, p.OrgID, p.HostID, p.ID, DeployQueued, p.SourceKind, p.GitURL, p.GitRef,
		p.ComposePath, p.InlineCompose, trigger, rollbackOf, startedBy,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Deploy{}, fmt.Errorf("store: CreateDeploy: %w", ErrActiveDeploy)
		}
		return Deploy{}, fmt.Errorf("store: CreateDeploy: %w", err)
	}
	return d, nil
}

// MarkDeployFailed sets a non-final deploy to failed (server-side: send errors,
// agent refusal, stale expiry). Ownership is checked like ApplyDeployEvent.
func (s *Store) MarkDeployFailed(ctx context.Context, orgID, hostID, deployID uuid.UUID, errText string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE deploys SET
			status = $4,
			error = $5,
			finished_at = COALESCE(finished_at, now()),
			last_event_at = now()
		WHERE id = $1 AND org_id = $2 AND host_id = $3
		  AND status = ANY($6)`,
		deployID, orgID, hostID, DeployFailed, clipDeployText(errText), runningStatuses)
	if err != nil {
		return fmt.Errorf("store: MarkDeployFailed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: MarkDeployFailed: %w", ErrNotFound)
	}
	return nil
}

// ApplyDeployEvent advances a deploy's phase. The deploy must belong to orgID
// and hostID (otherwise ErrNotFound). Stale or out-of-order phases are ignored.
func (s *Store) ApplyDeployEvent(ctx context.Context, orgID, hostID, deployID uuid.UUID, ev DeployEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: ApplyDeployEvent: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var cur string
	err = tx.QueryRow(ctx, `
		SELECT status FROM deploys
		WHERE id = $1 AND org_id = $2 AND host_id = $3
		FOR UPDATE`, deployID, orgID, hostID).Scan(&cur)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store: ApplyDeployEvent: %w", ErrNotFound)
		}
		return fmt.Errorf("store: ApplyDeployEvent: %w", err)
	}
	next := ev.Phase
	if next == "" || !phaseAllowed(cur, next) {
		// Still refresh last_event_at so ExpireStaleDeploys does not kill a
		// chatty-but-stuck agent mid-phase; only when the deploy is ours.
		_, _ = tx.Exec(ctx, `UPDATE deploys SET last_event_at = now() WHERE id = $1`, deployID)
		return tx.Commit(ctx)
	}

	msg := clipDeployText(ev.Message)
	commit := clipDeployText(ev.Commit)
	url := clipDeployText(ev.URL)
	errText := clipDeployText(ev.Error)

	started := cur == DeployQueued && next != DeployQueued
	finished := isFinalStatus(next)

	_, err = tx.Exec(ctx, `
		UPDATE deploys SET
			status = $2,
			message = CASE WHEN $3 <> '' THEN $3 ELSE message END,
			commit = CASE WHEN $4 <> '' THEN $4 ELSE commit END,
			url = CASE WHEN $5 <> '' THEN $5 ELSE url END,
			error = CASE WHEN $6 <> '' THEN $6 ELSE error END,
			started_at = CASE WHEN $7 AND started_at IS NULL THEN now() ELSE started_at END,
			finished_at = CASE WHEN $8 THEN now() ELSE finished_at END,
			last_event_at = now()
		WHERE id = $1`,
		deployID, next, msg, commit, url, errText, started, finished,
	)
	if err != nil {
		return fmt.Errorf("store: ApplyDeployEvent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: ApplyDeployEvent: %w", err)
	}
	return nil
}

// AppendDeployLog stores one log chunk. Ownership is checked; duplicate seq is
// ignored. Text beyond MaxDeployLogBytes per deploy is dropped and a final
// truncation marker is written once.
func (s *Store) AppendDeployLog(ctx context.Context, orgID, hostID, deployID uuid.UUID, log DeployLogChunk) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: AppendDeployLog: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var exists bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM deploys WHERE id = $1 AND org_id = $2 AND host_id = $3
		)`, deployID, orgID, hostID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("store: AppendDeployLog: %w", err)
	}
	if !exists {
		return fmt.Errorf("store: AppendDeployLog: %w", ErrNotFound)
	}

	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM deploy_logs WHERE deploy_id = $1 AND seq = $2`, deployID, log.Seq).Scan(&n)
	if err != nil {
		return fmt.Errorf("store: AppendDeployLog: %w", err)
	}
	if n > 0 {
		return tx.Commit(ctx) // idempotent
	}

	var total int64
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(octet_length(text)), 0) FROM deploy_logs WHERE deploy_id = $1`, deployID).Scan(&total)
	if err != nil {
		return fmt.Errorf("store: AppendDeployLog: %w", err)
	}
	if total >= MaxDeployLogBytes {
		return tx.Commit(ctx)
	}

	text := clipLogText(log.Text)
	stream := clipDeployText(log.Stream)
	if int64(len(text))+total > MaxDeployLogBytes {
		// Drop this chunk; ensure a truncation marker exists as the highest seq+1 style row.
		var hasMarker bool
		_ = tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM deploy_logs WHERE deploy_id = $1 AND text = $2)`,
			deployID, DeployLogTruncMsg).Scan(&hasMarker)
		if !hasMarker {
			var maxSeq int
			_ = tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM deploy_logs WHERE deploy_id = $1`, deployID).Scan(&maxSeq)
			_, err = tx.Exec(ctx, `
				INSERT INTO deploy_logs (deploy_id, seq, stream, text, withheld, org_id, host_id)
				VALUES ($1,$2,'',$3,false,$4,$5)
				ON CONFLICT DO NOTHING`,
				deployID, maxSeq+1, DeployLogTruncMsg, orgID, hostID)
			if err != nil {
				return fmt.Errorf("store: AppendDeployLog: %w", err)
			}
		}
		_, _ = tx.Exec(ctx, `UPDATE deploys SET last_event_at = now() WHERE id = $1`, deployID)
		return tx.Commit(ctx)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO deploy_logs (deploy_id, seq, stream, text, withheld, org_id, host_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT DO NOTHING`,
		deployID, log.Seq, stream, text, log.Withheld, orgID, hostID)
	if err != nil {
		return fmt.Errorf("store: AppendDeployLog: %w", err)
	}
	_, _ = tx.Exec(ctx, `UPDATE deploys SET last_event_at = now() WHERE id = $1`, deployID)
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: AppendDeployLog: %w", err)
	}
	return nil
}

// ListDeploys returns the newest deploys for a project (limit capped at 100).
func (s *Store) ListDeploys(ctx context.Context, orgID, projectID uuid.UUID, limit int) ([]Deploy, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+deployCols+` FROM deploys
		WHERE org_id = $1 AND project_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, orgID, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: ListDeploys: %w", err)
	}
	defer rows.Close()
	return collectDeploys(rows)
}

// GetDeploy returns one deploy in orgID.
func (s *Store) GetDeploy(ctx context.Context, orgID, id uuid.UUID) (Deploy, error) {
	d, err := scanDeploy(s.pool.QueryRow(ctx,
		`SELECT `+deployCols+` FROM deploys WHERE org_id = $1 AND id = $2`, orgID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Deploy{}, fmt.Errorf("store: GetDeploy: %w", ErrNotFound)
		}
		return Deploy{}, fmt.Errorf("store: GetDeploy: %w", err)
	}
	return d, nil
}

// ListDeployLogs returns log chunks with seq > afterSeq, oldest first.
// limit is capped at 500.
func (s *Store) ListDeployLogs(ctx context.Context, orgID, deployID uuid.UUID, afterSeq, limit int) ([]DeployLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT l.deploy_id, l.seq, l.stream, l.text, l.withheld
		FROM deploy_logs l
		JOIN deploys d ON d.id = l.deploy_id
		WHERE d.org_id = $1 AND l.deploy_id = $2 AND l.seq > $3
		ORDER BY l.seq ASC
		LIMIT $4`, orgID, deployID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("store: ListDeployLogs: %w", err)
	}
	defer rows.Close()
	var out []DeployLog
	for rows.Next() {
		var l DeployLog
		if err := rows.Scan(&l.DeployID, &l.Seq, &l.Stream, &l.Text, &l.Withheld); err != nil {
			return nil, fmt.Errorf("store: ListDeployLogs: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LastHealthyDeploy is the most recent deploy whose final status is healthy.
func (s *Store) LastHealthyDeploy(ctx context.Context, orgID, projectID uuid.UUID) (Deploy, error) {
	d, err := scanDeploy(s.pool.QueryRow(ctx, `
		SELECT `+deployCols+` FROM deploys
		WHERE org_id = $1 AND project_id = $2 AND status = $3
		ORDER BY finished_at DESC NULLS LAST, created_at DESC
		LIMIT 1`, orgID, projectID, DeployHealthy))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Deploy{}, fmt.Errorf("store: LastHealthyDeploy: %w", ErrNotFound)
		}
		return Deploy{}, fmt.Errorf("store: LastHealthyDeploy: %w", err)
	}
	return d, nil
}

// RunningDeploy returns the in-flight deploy for a project, if any.
func (s *Store) RunningDeploy(ctx context.Context, orgID, projectID uuid.UUID) (Deploy, error) {
	d, err := scanDeploy(s.pool.QueryRow(ctx, `
		SELECT `+deployCols+` FROM deploys
		WHERE org_id = $1 AND project_id = $2 AND status = ANY($3)
		ORDER BY created_at DESC
		LIMIT 1`, orgID, projectID, runningStatuses))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Deploy{}, fmt.Errorf("store: RunningDeploy: %w", ErrNotFound)
		}
		return Deploy{}, fmt.Errorf("store: RunningDeploy: %w", err)
	}
	return d, nil
}

// ExpireStaleDeploys marks running deploys with no event since olderThan before
// now as failed with "agent stopped reporting".
func (s *Store) ExpireStaleDeploys(ctx context.Context, now time.Time, olderThan time.Duration) (int, error) {
	cutoff := now.Add(-olderThan)
	tag, err := s.pool.Exec(ctx, `
		UPDATE deploys SET
			status = $1,
			error = 'agent stopped reporting',
			finished_at = $2,
			last_event_at = $2
		WHERE status = ANY($3)
		  AND COALESCE(last_event_at, created_at) < $4`,
		DeployFailed, now.UTC(), runningStatuses, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("store: ExpireStaleDeploys: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// PruneDeploys keeps the newest 50 deploys per project and the logs of the
// newest 20. Older rows are deleted.
func (s *Store) PruneDeploys(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		WITH ranked AS (
			SELECT id, project_id,
				row_number() OVER (PARTITION BY project_id ORDER BY created_at DESC, id DESC) AS rn
			FROM deploys
		)
		DELETE FROM deploys d USING ranked r
		WHERE d.id = r.id AND r.rn > 50`)
	if err != nil {
		return fmt.Errorf("store: PruneDeploys: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		WITH keep AS (
			SELECT id FROM (
				SELECT id, row_number() OVER (PARTITION BY project_id ORDER BY created_at DESC, id DESC) AS rn
				FROM deploys
			) x WHERE rn <= 20
		)
		DELETE FROM deploy_logs l
		WHERE NOT EXISTS (SELECT 1 FROM keep k WHERE k.id = l.deploy_id)`)
	if err != nil {
		return fmt.Errorf("store: PruneDeploys logs: %w", err)
	}
	return nil
}

func collectDeploys(rows pgx.Rows) ([]Deploy, error) {
	var out []Deploy
	for rows.Next() {
		d, err := scanDeploy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
