package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
// map[action]plaintextRef (16-char base64url). Server role.
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
		_, err = s.pool.Exec(ctx, `
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
		// Re-read plaintext is only available when we just generated it; if the
		// row was already used, Resolve will fail and delivery should skip.
		out[action] = ref
		// Persist the ref we just made when the upsert wrote it; if used, refresh from DB is N/A.
		_ = s.pool.QueryRow(ctx, `
			SELECT 1 FROM chat_action_refs
			WHERE message_id = $1 AND action = $2 AND ref_hash = $3 AND used_at IS NULL`,
			messageID, action, sum[:]).Scan(new(int))
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
	_, err = s.pool.Exec(ctx, `
		INSERT INTO chat_action_refs (org_id, message_id, action, ref_hash, expires_at)
		SELECT org_id, id, $2, $3, now() + interval '7 days'
		FROM chat_messages WHERE id = $1
		ON CONFLICT (message_id, action) DO NOTHING`,
		messageID, action, sum[:])
	if err != nil {
		return fmt.Errorf("store: ConsumeActionRef: ensure: %w", err)
	}

	tag, err := s.pool.Exec(ctx, `
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
	err = s.pool.QueryRow(ctx, `
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
