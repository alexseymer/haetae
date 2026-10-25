package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ActionRefPrefix is the Telegram callback_data prefix for opaque refs.
// Payload is "a:" + 16-char base64url (≤ 24 bytes total).
const ActionRefPrefix = "a:"

// ErrActionAlreadyUsed means the (message, action) receipt was already consumed.
var ErrActionAlreadyUsed = errors.New("store: chat action already used")

// CreateActionRefs stores one opaque ref per action for a message. Returns
// map[action]plaintextRef (16-char base64url). Actions already used are omitted
// so redelivery does not attach dead button refs. Server role.
func (s *Store) CreateActionRefs(ctx context.Context, orgID, messageID uuid.UUID, actions []string, expires time.Time) (map[string]string, error) {
	out := make(map[string]string, len(actions))
	for _, action := range actions {
		switch action {
		case "later", "mute30d", "ack":
		default:
			return nil, fmt.Errorf("store: CreateActionRefs: bad action %q", action)
		}
		ref, sum, err := newActionRef()
		if err != nil {
			return nil, err
		}
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO chat_action_refs (org_id, message_id, action, ref_hash, expires_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (message_id, action) DO UPDATE SET
				ref_hash = EXCLUDED.ref_hash,
				expires_at = EXCLUDED.expires_at,
				used_at = NULL
			WHERE chat_action_refs.used_at IS NULL`,
			orgID, messageID, action, sum[:], expires)
		if err != nil {
			return nil, fmt.Errorf("store: CreateActionRefs: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Row exists and is already used — skip so callers omit the button.
			continue
		}
		out[action] = ref
	}
	return out, nil
}

func newActionRef() (ref string, sum [sha256.Size]byte, err error) {
	var raw [12]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", sum, fmt.Errorf("store: action ref: %w", err)
	}
	ref = base64.RawURLEncoding.EncodeToString(raw[:]) // 16 chars
	sum = sha256.Sum256([]byte(ref))
	return ref, sum, nil
}

// ResolveActionRef looks up an opaque ref (without "a:" prefix) and returns
// message id + action when unused and unexpired. Does not consume.
func (s *Store) ResolveActionRef(ctx context.Context, ref string) (messageID uuid.UUID, action string, err error) {
	sum := sha256.Sum256([]byte(ref))
	err = s.pool.QueryRow(ctx, `
		SELECT message_id, action FROM chat_action_refs
		WHERE ref_hash = $1 AND used_at IS NULL AND expires_at > now()`,
		sum[:]).Scan(&messageID, &action)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", ErrActionRejected
		}
		return uuid.Nil, "", fmt.Errorf("store: ResolveActionRef: %w", err)
	}
	return messageID, action, nil
}

// ConsumeActionRef marks (message_id, action) used. Returns ErrActionAlreadyUsed
// when already consumed, ErrActionRejected when missing/expired.
// For HMAC paths that did not pre-create a ref, a receipt row is inserted first.
func (s *Store) ConsumeActionRef(ctx context.Context, messageID uuid.UUID, action string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: ConsumeActionRef: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := consumeActionRefTx(ctx, tx, messageID, action); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: ConsumeActionRef: %w", err)
	}
	return nil
}

func consumeActionRefTx(ctx context.Context, tx pgx.Tx, messageID uuid.UUID, action string) error {
	switch action {
	case "later", "mute30d", "ack":
	default:
		return ErrActionRejected
	}
	ref, sum, err := newActionRef()
	if err != nil {
		return err
	}
	_ = ref
	_, err = tx.Exec(ctx, `
		INSERT INTO chat_action_refs (org_id, message_id, action, ref_hash, expires_at)
		SELECT org_id, id, $2, $3, now() + interval '7 days'
		FROM chat_messages WHERE id = $1
		ON CONFLICT (message_id, action) DO NOTHING`,
		messageID, action, sum[:])
	if err != nil {
		return fmt.Errorf("store: ConsumeActionRef: ensure: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE chat_action_refs SET used_at = now()
		WHERE message_id = $1 AND action = $2
		  AND used_at IS NULL AND expires_at > now()`,
		messageID, action)
	if err != nil {
		return fmt.Errorf("store: ConsumeActionRef: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	var usedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT used_at FROM chat_action_refs WHERE message_id = $1 AND action = $2`,
		messageID, action).Scan(&usedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrActionRejected
		}
		return fmt.Errorf("store: ConsumeActionRef: %w", err)
	}
	if usedAt != nil {
		return ErrActionAlreadyUsed
	}
	return ErrActionRejected
}

// ApplyActionInbound consumes the action ref and inserts an inbound chat row in
// one transaction. On insert failure the ref stays usable.
func (s *Store) ApplyActionInbound(ctx context.Context, messageID uuid.UUID, action string, inbound ChatMessage) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: ApplyActionInbound: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := consumeActionRefTx(ctx, tx, messageID, action); err != nil {
		return err
	}
	if err := insertInboundTx(ctx, tx, inbound); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: ApplyActionInbound: %w", err)
	}
	return nil
}

// ApplyActionMute consumes the action ref, upserts mutes, and inserts an inbound
// row in one transaction. On any failure the ref stays usable.
func (s *Store) ApplyActionMute(ctx context.Context, messageID uuid.UUID, mutes []Mute, inbound ChatMessage) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: ApplyActionMute: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := consumeActionRefTx(ctx, tx, messageID, "mute30d"); err != nil {
		return err
	}
	for _, m := range mutes {
		if m.ID == uuid.Nil {
			id, err := uuid.NewRandom()
			if err != nil {
				return fmt.Errorf("store: ApplyActionMute: %w", err)
			}
			m.ID = id
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO mutes (id, org_id, host_id, rule, until, created_by)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (org_id, (COALESCE(host_id, '00000000-0000-0000-0000-000000000000'::uuid)), rule) DO UPDATE SET
				until = GREATEST(mutes.until, EXCLUDED.until)`,
			m.ID, m.OrgID, m.HostID, m.Rule, m.Until, m.CreatedBy)
		if err != nil {
			return fmt.Errorf("store: ApplyActionMute: mute: %w", err)
		}
	}
	if err := insertInboundTx(ctx, tx, inbound); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: ApplyActionMute: %w", err)
	}
	return nil
}

func insertInboundTx(ctx context.Context, tx pgx.Tx, m ChatMessage) error {
	m.Direction = "in"
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	actions, err := jsonMarshalActions(m.Actions)
	if err != nil {
		return err
	}
	evidence, err := jsonMarshalEvidence(m.Evidence)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO chat_messages (
			id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, attempts, next_attempt_at, delivery_error
		) VALUES (
			$1,$2,$3,$4,'in',$5,$6,$7,$8,$9,$10,$10,0,$10,''
		)`,
		m.ID, m.OrgID, m.Channel, m.AccountID, m.Kind, m.Body, actions, evidence,
		m.IncidentID, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: InsertInbound: %w", err)
	}
	return nil
}

func jsonMarshalActions(a []ChatAction) ([]byte, error) {
	if a == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("store: InsertInbound: %w", err)
	}
	return b, nil
}

func jsonMarshalEvidence(e []ChatEvidence) ([]byte, error) {
	if e == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("store: InsertInbound: %w", err)
	}
	return b, nil
}
