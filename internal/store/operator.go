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

// Defaults for an organisation without an org_settings row.
const (
	DefaultAIDailyTokenCap = 200000
	DefaultAutonomy        = "report"
)

// OperatorSettings are the per-organisation AI settings.
type OperatorSettings struct {
	AIEnabled     bool
	Autonomy      string // report | suggest | act
	DailyTokenCap int64
	// AIDigestEnabled lets the scheduled daily digest be posted to chat. It is
	// stored apart (SetAIDigestEnabled) so older callers of SetOperatorSettings
	// never switch it off by leaving it unset.
	AIDigestEnabled bool
}

// GetOperatorSettings returns the AI settings, or the safe defaults (off,
// report, 200 000 tokens) when the organisation has no settings row.
func (s *Store) GetOperatorSettings(ctx context.Context, orgID uuid.UUID) (OperatorSettings, error) {
	var o OperatorSettings
	err := s.pool.QueryRow(ctx, `
		SELECT ai_enabled, autonomy, ai_daily_token_cap, ai_digest_enabled FROM org_settings WHERE org_id = $1`, orgID,
	).Scan(&o.AIEnabled, &o.Autonomy, &o.DailyTokenCap, &o.AIDigestEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorSettings{Autonomy: DefaultAutonomy, DailyTokenCap: DefaultAIDailyTokenCap, AIDigestEnabled: true}, nil
	}
	if err != nil {
		return OperatorSettings{}, fmt.Errorf("store: GetOperatorSettings: %w", err)
	}
	return o, nil
}

// SetOperatorSettings stores the AI settings (creating the settings row).
func (s *Store) SetOperatorSettings(ctx context.Context, orgID uuid.UUID, o OperatorSettings) error {
	switch o.Autonomy {
	case "report", "suggest", "act":
	default:
		return errors.New("store: unknown autonomy level")
	}
	if o.DailyTokenCap < 0 {
		return errors.New("store: negative token cap")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO org_settings (org_id, ai_enabled, autonomy, ai_daily_token_cap)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id) DO UPDATE SET
			ai_enabled = EXCLUDED.ai_enabled,
			autonomy = EXCLUDED.autonomy,
			ai_daily_token_cap = EXCLUDED.ai_daily_token_cap,
			updated_at = now()`,
		orgID, o.AIEnabled, o.Autonomy, o.DailyTokenCap)
	if err != nil {
		return fmt.Errorf("store: SetOperatorSettings: %w", err)
	}
	return nil
}

// SetAIDigestEnabled stores whether the scheduled daily digest is posted
// (creating the settings row).
func (s *Store) SetAIDigestEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO org_settings (org_id, ai_digest_enabled) VALUES ($1, $2)
		ON CONFLICT (org_id) DO UPDATE SET ai_digest_enabled = EXCLUDED.ai_digest_enabled, updated_at = now()`,
		orgID, enabled)
	if err != nil {
		return fmt.Errorf("store: SetAIDigestEnabled: %w", err)
	}
	return nil
}

// AddUsage adds token usage to the organisation's row for the UTC day of at.
func (s *Store) AddUsage(ctx context.Context, orgID uuid.UUID, at time.Time, in, out int64) error {
	if in < 0 || out < 0 {
		return errors.New("store: negative usage")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_usage (org_id, day, input_tokens, output_tokens, calls)
		VALUES ($1, ($2::timestamptz AT TIME ZONE 'UTC')::date, $3, $4, 1)
		ON CONFLICT (org_id, day) DO UPDATE SET
			input_tokens = ai_usage.input_tokens + EXCLUDED.input_tokens,
			output_tokens = ai_usage.output_tokens + EXCLUDED.output_tokens,
			calls = ai_usage.calls + 1`,
		orgID, at, in, out)
	if err != nil {
		return fmt.Errorf("store: AddUsage: %w", err)
	}
	return nil
}

// Usage is one UTC day of token usage.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	Calls        int
}

// Total is input plus output tokens.
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

// UsageToday returns the usage for the UTC day of now (zero when none).
func (s *Store) UsageToday(ctx context.Context, orgID uuid.UUID, now time.Time) (Usage, error) {
	var u Usage
	err := s.pool.QueryRow(ctx, `
		SELECT input_tokens, output_tokens, calls FROM ai_usage
		WHERE org_id = $1 AND day = ($2::timestamptz AT TIME ZONE 'UTC')::date`, orgID, now,
	).Scan(&u.InputTokens, &u.OutputTokens, &u.Calls)
	if errors.Is(err, pgx.ErrNoRows) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("store: UsageToday: %w", err)
	}
	return u, nil
}

// AIEvidence is one stored tool result.
type AIEvidence struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	ConversationID uuid.UUID
	Seq            int
	Kind           string
	Trusted        bool
	SubjectType    string
	SubjectID      string
	Title          string
	Summary        string
	Data           json.RawMessage
	Link           string
	CreatedAt      time.Time
}

// InsertEvidence stores e (OrgID, ConversationID, Seq set by the caller) and
// returns it with ID and CreatedAt. (conversation, seq) is unique.
func (s *Store) InsertEvidence(ctx context.Context, e AIEvidence) (AIEvidence, error) {
	if len(e.Data) == 0 {
		e.Data = json.RawMessage(`{}`)
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_evidence (org_id, conversation_id, seq, kind, trusted, subject_type, subject_id,
			title, summary, data, link)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, created_at`,
		e.OrgID, e.ConversationID, e.Seq, e.Kind, e.Trusted, e.SubjectType, e.SubjectID,
		e.Title, e.Summary, []byte(e.Data), e.Link,
	).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		return AIEvidence{}, fmt.Errorf("store: InsertEvidence: %w", err)
	}
	return e, nil
}

// GetEvidence returns the evidence of one conversation in seq order.
func (s *Store) GetEvidence(ctx context.Context, orgID, conversationID uuid.UUID) ([]AIEvidence, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, conversation_id, seq, kind, trusted, subject_type, subject_id,
			title, summary, data, link, created_at
		FROM ai_evidence WHERE org_id = $1 AND conversation_id = $2 ORDER BY seq`, orgID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("store: GetEvidence: %w", err)
	}
	defer rows.Close()
	var out []AIEvidence
	for rows.Next() {
		var e AIEvidence
		var data []byte
		if err := rows.Scan(&e.ID, &e.OrgID, &e.ConversationID, &e.Seq, &e.Kind, &e.Trusted,
			&e.SubjectType, &e.SubjectID, &e.Title, &e.Summary, &data, &e.Link, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: GetEvidence: %w", err)
		}
		e.Data = data
		out = append(out, e)
	}
	return out, rows.Err()
}

// Action outcomes and origins stored in ai_actions.
const (
	ActionRefused          = "refused"
	ActionAwaitingApproval = "awaiting_approval"
	ActionSent             = "sent"
	ActionSucceeded        = "succeeded"
	ActionFailed           = "failed"

	ActionOriginProposed = "proposed"
	ActionOriginApproved = "approved"
	ActionOriginAuto     = "auto"
)

// AIAction is the audit row of one proposed or executed operator action.
type AIAction struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	HostID      *uuid.UUID
	ContainerID string
	Kind        string
	Level       string
	Origin      string
	Outcome     string
	Reason      string
	EvidenceIDs []uuid.UUID
	Detail      string
	CreatedAt   time.Time
}

const aiActionCols = `id, org_id, host_id, container_id, kind, level, origin, outcome, reason, evidence_ids, detail, created_at`

func scanAIAction(row pgx.Row) (AIAction, error) {
	var a AIAction
	err := row.Scan(&a.ID, &a.OrgID, &a.HostID, &a.ContainerID, &a.Kind, &a.Level, &a.Origin,
		&a.Outcome, &a.Reason, &a.EvidenceIDs, &a.Detail, &a.CreatedAt)
	return a, err
}

// InsertAction writes an audit row and returns it with ID and CreatedAt.
func (s *Store) InsertAction(ctx context.Context, a AIAction) (AIAction, error) {
	if a.EvidenceIDs == nil {
		a.EvidenceIDs = []uuid.UUID{}
	}
	if a.Kind == "" {
		a.Kind = "restart_container"
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_actions (org_id, host_id, container_id, kind, level, origin, outcome, reason, evidence_ids, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, created_at`,
		a.OrgID, a.HostID, a.ContainerID, a.Kind, a.Level, a.Origin, a.Outcome, a.Reason, a.EvidenceIDs, a.Detail,
	).Scan(&a.ID, &a.CreatedAt)
	if err != nil {
		return AIAction{}, fmt.Errorf("store: InsertAction: %w", err)
	}
	return a, nil
}

// UpdateActionOutcome sets the outcome and detail of an action of the org.
func (s *Store) UpdateActionOutcome(ctx context.Context, orgID, id uuid.UUID, outcome, detail string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE ai_actions SET outcome = $3, detail = $4 WHERE org_id = $1 AND id = $2`,
		orgID, id, outcome, detail)
	if err != nil {
		return fmt.Errorf("store: UpdateActionOutcome: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAction returns one action of the org, or ErrNotFound.
func (s *Store) GetAction(ctx context.Context, orgID, id uuid.UUID) (AIAction, error) {
	a, err := scanAIAction(s.pool.QueryRow(ctx,
		`SELECT `+aiActionCols+` FROM ai_actions WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AIAction{}, ErrNotFound
	}
	if err != nil {
		return AIAction{}, fmt.Errorf("store: GetAction: %w", err)
	}
	return a, nil
}

// ClaimApprovedAction atomically moves an awaiting_approval action to sent
// with origin approved. Only one caller can win; the others get ErrNotFound.
func (s *Store) ClaimApprovedAction(ctx context.Context, orgID, id uuid.UUID) (AIAction, error) {
	a, err := scanAIAction(s.pool.QueryRow(ctx, `
		UPDATE ai_actions SET outcome = 'sent', origin = 'approved'
		WHERE org_id = $1 AND id = $2 AND outcome = 'awaiting_approval'
		RETURNING `+aiActionCols, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AIAction{}, ErrNotFound
	}
	if err != nil {
		return AIAction{}, fmt.Errorf("store: ClaimApprovedAction: %w", err)
	}
	return a, nil
}

// RefuseAwaitingAction marks a still-awaiting action refused (single use).
func (s *Store) RefuseAwaitingAction(ctx context.Context, orgID, id uuid.UUID, detail string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE ai_actions SET outcome = 'refused', detail = $3
		WHERE org_id = $1 AND id = $2 AND outcome = 'awaiting_approval'`, orgID, id, detail)
	if err != nil {
		return fmt.Errorf("store: RefuseAwaitingAction: %w", err)
	}
	return nil
}

// CountRecentRestarts counts restarts the operator actually dispatched
// (outcome sent, succeeded or failed) for the container since the given time.
func (s *Store) CountRecentRestarts(ctx context.Context, orgID uuid.UUID, containerID string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM ai_actions
		WHERE org_id = $1 AND container_id = $2 AND kind = 'restart_container'
		  AND outcome IN ('sent','succeeded','failed') AND created_at >= $3`,
		orgID, containerID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: CountRecentRestarts: %w", err)
	}
	return n, nil
}
