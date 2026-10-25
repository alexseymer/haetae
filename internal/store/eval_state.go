package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AlertEvalKind values for alert_eval_state.kind.
const (
	EvalKindUnhealthy   = "unhealthy"
	EvalKindRestart     = "restart"
	EvalKindFalseStreak = "false_streak"
)

// AlertEvalState is one persisted evaluator tracker row.
type AlertEvalState struct {
	OrgID           uuid.UUID
	Kind            string
	Key             string
	FirstSeen       *time.Time
	RestartBaseline *int
	FalseStreak     int
	Meta            json.RawMessage
	UpdatedAt       time.Time
}

// LoadAlertEvalState returns all tracker rows for an organisation.
func (s *Store) LoadAlertEvalState(ctx context.Context, orgID uuid.UUID) ([]AlertEvalState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, kind, key, first_seen, restart_baseline, false_streak, meta, updated_at
		FROM alert_eval_state WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: LoadAlertEvalState: %w", err)
	}
	defer rows.Close()
	var out []AlertEvalState
	for rows.Next() {
		var st AlertEvalState
		if err := rows.Scan(&st.OrgID, &st.Kind, &st.Key, &st.FirstSeen, &st.RestartBaseline,
			&st.FalseStreak, &st.Meta, &st.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: LoadAlertEvalState: %w", err)
		}
		if len(st.Meta) == 0 {
			st.Meta = json.RawMessage(`{}`)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// UpsertAlertEvalState writes one tracker row.
func (s *Store) UpsertAlertEvalState(ctx context.Context, st AlertEvalState) error {
	if st.Meta == nil {
		st.Meta = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_eval_state (
			org_id, kind, key, first_seen, restart_baseline, false_streak, meta, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (org_id, kind, key) DO UPDATE SET
			first_seen = EXCLUDED.first_seen,
			restart_baseline = EXCLUDED.restart_baseline,
			false_streak = EXCLUDED.false_streak,
			meta = EXCLUDED.meta,
			updated_at = now()`,
		st.OrgID, st.Kind, st.Key, st.FirstSeen, st.RestartBaseline, st.FalseStreak, st.Meta)
	if err != nil {
		return fmt.Errorf("store: UpsertAlertEvalState: %w", err)
	}
	return nil
}

// DeleteAlertEvalState removes a tracker row.
func (s *Store) DeleteAlertEvalState(ctx context.Context, orgID uuid.UUID, kind, key string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM alert_eval_state WHERE org_id = $1 AND kind = $2 AND key = $3`,
		orgID, kind, key)
	if err != nil {
		return fmt.Errorf("store: DeleteAlertEvalState: %w", err)
	}
	return nil
}

// SaveUnhealthySince persists when a container first became unhealthy.
func (s *Store) SaveUnhealthySince(ctx context.Context, orgID uuid.UUID, key string, since time.Time) error {
	return s.UpsertAlertEvalState(ctx, AlertEvalState{
		OrgID: orgID, Kind: EvalKindUnhealthy, Key: key, FirstSeen: &since,
	})
}

// SaveRestartBaseline persists the first restart_count observation for crash_loop.
func (s *Store) SaveRestartBaseline(ctx context.Context, orgID uuid.UUID, key string, baseline int, at time.Time) error {
	meta, _ := json.Marshal(map[string]any{"at": at.UTC().Format(time.RFC3339Nano)})
	return s.UpsertAlertEvalState(ctx, AlertEvalState{
		OrgID: orgID, Kind: EvalKindRestart, Key: key, RestartBaseline: &baseline, Meta: meta,
	})
}

// SaveFalseStreak persists consecutive-false resolution counter for a dedupe key.
func (s *Store) SaveFalseStreak(ctx context.Context, orgID uuid.UUID, dedupeKey string, n int) error {
	if n <= 0 {
		return s.DeleteAlertEvalState(ctx, orgID, EvalKindFalseStreak, dedupeKey)
	}
	return s.UpsertAlertEvalState(ctx, AlertEvalState{
		OrgID: orgID, Kind: EvalKindFalseStreak, Key: dedupeKey, FalseStreak: n,
	})
}
