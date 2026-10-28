package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Chat delivery and link-code constants.
const (
	MaxDeliveryAttempts = 8
	LinkCodeLen         = 8
	LinkCodeTTL         = 10 * time.Minute
	LinkCodeRateMax     = 5
	LinkCodeRateWindow  = 10 * time.Minute
	ActionTokenTTL      = 7 * 24 * time.Hour
	ChatNotifyChannel   = "haetae_chat"
)

// 32-symbol alphabet for link codes (no look-alikes). Rejection sampling
// keeps each character equally likely.
const linkCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// Chat store errors.
var (
	ErrLinkCodeInvalid   = errors.New("store: link code invalid")
	ErrLinkCodeRateLimit = errors.New("store: link code rate limit")
	ErrAccountTaken      = errors.New("store: chat account already linked")
	ErrActionRejected    = errors.New("store: chat action rejected")
)

// EnqueueChatMessage inserts an outbound chat message (server role) and
// notifies the delivery loop. When IdempotencyKey is set, a second insert with
// the same key is a no-op that returns the existing row (no duplicate outbound).
func (s *Store) EnqueueChatMessage(ctx context.Context, m ChatMessage) (ChatMessage, error) {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	if m.Direction == "" {
		m.Direction = "out"
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.NextAttemptAt.IsZero() {
		m.NextAttemptAt = m.CreatedAt
	}
	actions, err := json.Marshal(m.Actions)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: EnqueueChatMessage: actions: %w", err)
	}
	if m.Actions == nil {
		actions = []byte("[]")
	}
	evidence, err := json.Marshal(m.Evidence)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: EnqueueChatMessage: evidence: %w", err)
	}
	if m.Evidence == nil {
		evidence = []byte("[]")
	}
	meta, err := marshalChatMeta(m.Meta)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: EnqueueChatMessage: meta: %w", err)
	}
	var idemArg any
	if m.IdempotencyKey != "" {
		idemArg = m.IdempotencyKey
	}
	var idemOut *string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO chat_messages (
			id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, attempts, next_attempt_at, delivery_error, idempotency_key, meta
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16
		)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, send_started_at, sent_at,
			attempts, next_attempt_at, delivery_error, idempotency_key, meta`,
		m.ID, m.OrgID, m.Channel, m.AccountID, m.Direction, m.Kind, m.Body, actions, evidence,
		m.IncidentID, m.CreatedAt, m.Attempts, m.NextAttemptAt, m.DeliveryError, idemArg, meta,
	).Scan(
		&m.ID, &m.OrgID, &m.Channel, &m.AccountID, &m.Direction, &m.Kind, &m.Body,
		&actions, &evidence, &m.IncidentID, &m.CreatedAt, &m.DeliveredAt,
		&m.SendStartedAt, &m.SentAt,
		&m.Attempts, &m.NextAttemptAt, &m.DeliveryError, &idemOut, &meta,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && m.IdempotencyKey != "" {
			// Conflict: return the existing row so callers treat it as success.
			existing, gerr := s.getChatMessageByIdempotency(ctx, m.IdempotencyKey)
			if gerr != nil {
				return ChatMessage{}, fmt.Errorf("store: EnqueueChatMessage: %w", gerr)
			}
			return existing, nil
		}
		return ChatMessage{}, fmt.Errorf("store: EnqueueChatMessage: %w", err)
	}
	if idemOut != nil {
		m.IdempotencyKey = *idemOut
	} else {
		m.IdempotencyKey = ""
	}
	if err := unmarshalChatJSON(actions, evidence, &m); err != nil {
		return ChatMessage{}, err
	}
	if err := unmarshalChatMeta(meta, &m); err != nil {
		return ChatMessage{}, err
	}
	if _, err := s.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, ChatNotifyChannel, m.Channel); err != nil {
		return ChatMessage{}, fmt.Errorf("store: EnqueueChatMessage: notify: %w", err)
	}
	return m, nil
}

func (s *Store) getChatMessageByIdempotency(ctx context.Context, key string) (ChatMessage, error) {
	var m ChatMessage
	var actions, evidence []byte
	var idem *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, send_started_at, sent_at,
			attempts, next_attempt_at, delivery_error, idempotency_key
		FROM chat_messages WHERE idempotency_key = $1`, key,
	).Scan(
		&m.ID, &m.OrgID, &m.Channel, &m.AccountID, &m.Direction, &m.Kind, &m.Body,
		&actions, &evidence, &m.IncidentID, &m.CreatedAt, &m.DeliveredAt,
		&m.SendStartedAt, &m.SentAt,
		&m.Attempts, &m.NextAttemptAt, &m.DeliveryError, &idem,
	)
	if err != nil {
		return ChatMessage{}, err
	}
	if idem != nil {
		m.IdempotencyKey = *idem
	}
	if err := unmarshalChatJSON(actions, evidence, &m); err != nil {
		return ChatMessage{}, err
	}
	return m, nil
}

// ClaimDueChatMessages claims up to limit outbound rows that are due for
// delivery, using FOR UPDATE SKIP LOCKED. Server role only.
func (s *Store) ClaimDueChatMessages(ctx context.Context, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 32
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: ClaimDueChatMessages: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, send_started_at, sent_at,
			attempts, next_attempt_at, delivery_error, meta
		FROM chat_messages
		WHERE direction = 'out'
		  AND delivered_at IS NULL
		  AND (attempts < $1 OR sent_at IS NOT NULL)
		  AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		FOR UPDATE SKIP LOCKED
		LIMIT $2`, MaxDeliveryAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("store: ClaimDueChatMessages: %w", err)
	}
	out, err := scanChatMessages(rows)
	if err != nil {
		return nil, fmt.Errorf("store: ClaimDueChatMessages: %w", err)
	}
	// Bump next_attempt_at slightly so a crashed worker does not reclaim
	// immediately while we still hold the row set only for this tx.
	for _, m := range out {
		if _, err := tx.Exec(ctx, `
			UPDATE chat_messages SET next_attempt_at = now() + interval '30 seconds'
			WHERE id = $1 AND delivered_at IS NULL`, m.ID); err != nil {
			return nil, fmt.Errorf("store: ClaimDueChatMessages: hold: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: ClaimDueChatMessages: %w", err)
	}
	return out, nil
}

// MarkDelivered sets delivered_at on an outbound message.
func (s *Store) MarkDelivered(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat_messages SET delivered_at = $2, delivery_error = ''
		WHERE id = $1 AND delivered_at IS NULL`, id, at)
	if err != nil {
		return fmt.Errorf("store: MarkDelivered: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: MarkDelivered: %w", ErrNotFound)
	}
	return nil
}

// MarkSent records that channel Send succeeded (best-effort persist so a process
// restart does not resend). Retries are the caller's responsibility.
func (s *Store) MarkSent(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat_messages
		SET sent_at = COALESCE(sent_at, $2),
		    send_started_at = COALESCE(send_started_at, $2)
		WHERE id = $1 AND delivered_at IS NULL`, id, at)
	if err != nil {
		return fmt.Errorf("store: MarkSent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: MarkSent: %w", ErrNotFound)
	}
	return nil
}

// MarkFailed records a delivery failure with exponential backoff. After
// MaxDeliveryAttempts the row stays undelivered and is no longer claimed.
func (s *Store) MarkFailed(ctx context.Context, id uuid.UUID, attempts int, errMsg string, now time.Time) error {
	if attempts < 1 {
		attempts = 1
	}
	backoff := deliveryBackoff(attempts)
	next := now.Add(backoff)
	if attempts >= MaxDeliveryAttempts {
		// Far future: keep the row visible but stop claiming it.
		next = now.Add(100 * 365 * 24 * time.Hour)
	}
	if len(errMsg) > 2000 {
		errMsg = errMsg[:2000]
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat_messages
		SET attempts = $2, next_attempt_at = $3, delivery_error = $4
		WHERE id = $1 AND delivered_at IS NULL`, id, attempts, next, errMsg)
	if err != nil {
		return fmt.Errorf("store: MarkFailed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: MarkFailed: %w", ErrNotFound)
	}
	return nil
}

func deliveryBackoff(attempts int) time.Duration {
	// 1s, 2s, 4s, … capped at 5 minutes.
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 8 {
		shift = 8
	}
	d := time.Duration(1<<uint(shift)) * time.Second
	if d > 5*time.Minute {
		return 5 * time.Minute
	}
	return d
}

// ListMessages returns web-channel messages for an organisation after an
// optional cursor, newest last. Use ForOrg for tenant isolation.
func (s *Store) ListMessages(ctx context.Context, orgID uuid.UUID, after *uuid.UUID, limit int) ([]ChatMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		rows pgx.Rows
		err  error
	)
	if after == nil {
		rows, err = s.pool.Query(ctx, `
			SELECT id, org_id, channel, account_id, direction, kind, body, actions, evidence,
				incident_id, created_at, delivered_at, send_started_at, sent_at,
				attempts, next_attempt_at, delivery_error, meta
			FROM chat_messages
			WHERE org_id = $1 AND channel = $2
			ORDER BY created_at ASC, id ASC
			LIMIT $3`, orgID, ChannelWeb, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, org_id, channel, account_id, direction, kind, body, actions, evidence,
				incident_id, created_at, delivered_at, send_started_at, sent_at,
				attempts, next_attempt_at, delivery_error, meta
			FROM chat_messages
			WHERE org_id = $1 AND channel = $2
			  AND (created_at, id) > (
				SELECT created_at, id FROM chat_messages WHERE id = $3 AND org_id = $1
			  )
			ORDER BY created_at ASC, id ASC
			LIMIT $4`, orgID, ChannelWeb, *after, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("store: ListMessages: %w", err)
	}
	out, err := scanChatMessages(rows)
	if err != nil {
		return nil, fmt.Errorf("store: ListMessages: %w", err)
	}
	return out, nil
}

// InsertInbound stores an inbound chat message.
func (s *Store) InsertInbound(ctx context.Context, m ChatMessage) (ChatMessage, error) {
	m.Direction = "in"
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	m.NextAttemptAt = m.CreatedAt
	actions, err := json.Marshal(m.Actions)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: InsertInbound: %w", err)
	}
	if m.Actions == nil {
		actions = []byte("[]")
	}
	evidence, err := json.Marshal(m.Evidence)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: InsertInbound: %w", err)
	}
	if m.Evidence == nil {
		evidence = []byte("[]")
	}
	// Mark inbound as already "delivered" so the outbox never picks it up.
	now := m.CreatedAt
	err = s.pool.QueryRow(ctx, `
		INSERT INTO chat_messages (
			id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, attempts, next_attempt_at, delivery_error
		) VALUES (
			$1,$2,$3,$4,'in',$5,$6,$7,$8,$9,$10,$11,0,$10,''
		)
		RETURNING id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, send_started_at, sent_at,
			attempts, next_attempt_at, delivery_error`,
		m.ID, m.OrgID, m.Channel, m.AccountID, m.Kind, m.Body, actions, evidence,
		m.IncidentID, m.CreatedAt, now,
	).Scan(
		&m.ID, &m.OrgID, &m.Channel, &m.AccountID, &m.Direction, &m.Kind, &m.Body,
		&actions, &evidence, &m.IncidentID, &m.CreatedAt, &m.DeliveredAt,
		&m.SendStartedAt, &m.SentAt,
		&m.Attempts, &m.NextAttemptAt, &m.DeliveryError,
	)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: InsertInbound: %w", err)
	}
	if err := unmarshalChatJSON(actions, evidence, &m); err != nil {
		return ChatMessage{}, err
	}
	return m, nil
}

// CreateChatAccount inserts a verified chat account binding.
func (s *Store) CreateChatAccount(ctx context.Context, orgID, userID uuid.UUID, channel, externalID string) (ChatAccount, error) {
	var a ChatAccount
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat_accounts (org_id, user_id, channel, external_id)
		VALUES ($1,$2,$3,$4)
		RETURNING id, org_id, user_id, channel, external_id, verified_at, created_at`,
		orgID, userID, channel, externalID,
	).Scan(&a.ID, &a.OrgID, &a.UserID, &a.Channel, &a.ExternalID, &a.VerifiedAt, &a.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ChatAccount{}, fmt.Errorf("store: CreateChatAccount: %w", ErrAccountTaken)
		}
		return ChatAccount{}, fmt.Errorf("store: CreateChatAccount: %w", err)
	}
	return a, nil
}

// ListChatAccounts returns all chat accounts for an organisation.
func (s *Store) ListChatAccounts(ctx context.Context, orgID uuid.UUID) ([]ChatAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, user_id, channel, external_id, verified_at, created_at
		FROM chat_accounts WHERE org_id = $1
		ORDER BY created_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: ListChatAccounts: %w", err)
	}
	defer rows.Close()
	var out []ChatAccount
	for rows.Next() {
		var a ChatAccount
		if err := rows.Scan(&a.ID, &a.OrgID, &a.UserID, &a.Channel, &a.ExternalID, &a.VerifiedAt, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: ListChatAccounts: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListVerifiedAccounts returns verified accounts for one channel in an org.
func (s *Store) ListVerifiedAccounts(ctx context.Context, orgID uuid.UUID, channel string) ([]ChatAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, user_id, channel, external_id, verified_at, created_at
		FROM chat_accounts
		WHERE org_id = $1 AND channel = $2 AND verified_at IS NOT NULL
		ORDER BY created_at`, orgID, channel)
	if err != nil {
		return nil, fmt.Errorf("store: ListVerifiedAccounts: %w", err)
	}
	defer rows.Close()
	var out []ChatAccount
	for rows.Next() {
		var a ChatAccount
		if err := rows.Scan(&a.ID, &a.OrgID, &a.UserID, &a.Channel, &a.ExternalID, &a.VerifiedAt, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: ListVerifiedAccounts: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListVerifiedAdminAccounts returns verified accounts for one channel in an
// org whose user is an enabled organisation admin.
func (s *Store) ListVerifiedAdminAccounts(ctx context.Context, orgID uuid.UUID, channel string) ([]ChatAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.org_id, a.user_id, a.channel, a.external_id, a.verified_at, a.created_at
		FROM chat_accounts a
		JOIN users u ON u.id = a.user_id AND u.org_id = a.org_id
		WHERE a.org_id = $1 AND a.channel = $2 AND a.verified_at IS NOT NULL
		  AND u.role = 'admin' AND u.disabled_at IS NULL
		ORDER BY a.created_at`, orgID, channel)
	if err != nil {
		return nil, fmt.Errorf("store: ListVerifiedAdminAccounts: %w", err)
	}
	defer rows.Close()
	var out []ChatAccount
	for rows.Next() {
		var a ChatAccount
		if err := rows.Scan(&a.ID, &a.OrgID, &a.UserID, &a.Channel, &a.ExternalID, &a.VerifiedAt, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: ListVerifiedAdminAccounts: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetChatAccountByExternal looks up a verified account by channel identity.
func (s *Store) GetChatAccountByExternal(ctx context.Context, channel, externalID string) (ChatAccount, error) {
	var a ChatAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, user_id, channel, external_id, verified_at, created_at
		FROM chat_accounts WHERE channel = $1 AND external_id = $2`, channel, externalID,
	).Scan(&a.ID, &a.OrgID, &a.UserID, &a.Channel, &a.ExternalID, &a.VerifiedAt, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChatAccount{}, fmt.Errorf("store: GetChatAccountByExternal: %w", ErrNotFound)
		}
		return ChatAccount{}, fmt.Errorf("store: GetChatAccountByExternal: %w", err)
	}
	return a, nil
}

// GetChatAccount returns an account by id within an organisation.
func (s *Store) GetChatAccount(ctx context.Context, orgID, id uuid.UUID) (ChatAccount, error) {
	var a ChatAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, user_id, channel, external_id, verified_at, created_at
		FROM chat_accounts WHERE org_id = $1 AND id = $2`, orgID, id,
	).Scan(&a.ID, &a.OrgID, &a.UserID, &a.Channel, &a.ExternalID, &a.VerifiedAt, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChatAccount{}, fmt.Errorf("store: GetChatAccount: %w", ErrNotFound)
		}
		return ChatAccount{}, fmt.Errorf("store: GetChatAccount: %w", err)
	}
	return a, nil
}

// DeleteChatAccount removes a chat account.
func (s *Store) DeleteChatAccount(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM chat_accounts WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("store: DeleteChatAccount: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: DeleteChatAccount: %w", ErrNotFound)
	}
	return nil
}

// CreateLinkCode creates a one-time link code for a user and channel. The
// plaintext code is returned once; only its SHA-256 is stored. Rate limit is
// enforced atomically (advisory lock per user). Server role.
func (s *Store) CreateLinkCode(ctx context.Context, orgID, userID uuid.UUID, channel string) (code string, err error) {
	switch channel {
	case ChannelTelegram, ChannelSlack, ChannelEmail, ChannelSignal:
	default:
		return "", fmt.Errorf("store: CreateLinkCode: unsupported channel %q", channel)
	}
	code, err = newLinkCode(LinkCodeLen)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.ToUpper(code)))
	expires := time.Now().UTC().Add(LinkCodeTTL)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("store: CreateLinkCode: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Per-user advisory lock so concurrent POSTs cannot exceed the rate.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, userID.String()); err != nil {
		return "", fmt.Errorf("store: CreateLinkCode: lock: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM chat_link_codes
		WHERE user_id = $1 AND expires_at > now()`, userID).Scan(&n); err != nil {
		return "", fmt.Errorf("store: CreateLinkCode: %w", err)
	}
	if n >= LinkCodeRateMax {
		return "", ErrLinkCodeRateLimit
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO chat_link_codes (code_hash, org_id, user_id, channel, expires_at)
		VALUES ($1,$2,$3,$4,$5)`, sum[:], orgID, userID, channel, expires)
	if err != nil {
		return "", fmt.Errorf("store: CreateLinkCode: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("store: CreateLinkCode: %w", err)
	}
	return code, nil
}

// ConsumeLinkCode looks up a valid unused code without marking it used.
// Prefer LinkChatAccount, which validates channel and creates the account in
// one transaction so a mismatch or insert failure leaves the code usable.
func (s *Store) ConsumeLinkCode(ctx context.Context, code string) (orgID, userID uuid.UUID, channel string, err error) {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	err = s.pool.QueryRow(ctx, `
		SELECT org_id, user_id, channel FROM chat_link_codes
		WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()`,
		sum[:]).Scan(&orgID, &userID, &channel)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, uuid.Nil, "", ErrLinkCodeInvalid
		}
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("store: ConsumeLinkCode: %w", err)
	}
	return orgID, userID, channel, nil
}

// LinkChatAccount validates channel match, creates the chat account, and marks
// the link code used in one transaction. Wrong channel or insert failure leaves
// the code unused.
func (s *Store) LinkChatAccount(ctx context.Context, code, channel, externalID string) (ChatAccount, error) {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ChatAccount{}, fmt.Errorf("store: LinkChatAccount: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var orgID, userID uuid.UUID
	var wantChannel string
	err = tx.QueryRow(ctx, `
		SELECT org_id, user_id, channel FROM chat_link_codes
		WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
		FOR UPDATE`, sum[:]).Scan(&orgID, &userID, &wantChannel)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChatAccount{}, ErrLinkCodeInvalid
		}
		return ChatAccount{}, fmt.Errorf("store: LinkChatAccount: %w", err)
	}
	if wantChannel != channel {
		// Do not mark used — caller may retry on the correct channel.
		return ChatAccount{}, ErrLinkCodeInvalid
	}

	var a ChatAccount
	err = tx.QueryRow(ctx, `
		INSERT INTO chat_accounts (org_id, user_id, channel, external_id)
		VALUES ($1,$2,$3,$4)
		RETURNING id, org_id, user_id, channel, external_id, verified_at, created_at`,
		orgID, userID, channel, externalID,
	).Scan(&a.ID, &a.OrgID, &a.UserID, &a.Channel, &a.ExternalID, &a.VerifiedAt, &a.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ChatAccount{}, ErrAccountTaken
		}
		return ChatAccount{}, fmt.Errorf("store: LinkChatAccount: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE chat_link_codes SET used_at = now()
		WHERE code_hash = $1 AND used_at IS NULL`, sum[:])
	if err != nil {
		return ChatAccount{}, fmt.Errorf("store: LinkChatAccount: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ChatAccount{}, ErrLinkCodeInvalid
	}
	if err := tx.Commit(ctx); err != nil {
		return ChatAccount{}, fmt.Errorf("store: LinkChatAccount: %w", err)
	}
	return a, nil
}

// FindOutboundByShortRef finds an outbound message whose id hex prefix matches
// ref (at least 8 hex characters, dashes ignored) within an org and channel.
func (s *Store) FindOutboundByShortRef(ctx context.Context, orgID uuid.UUID, channel, ref string) (ChatMessage, error) {
	ref = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(ref), "-", ""))
	if len(ref) < 8 {
		return ChatMessage{}, fmt.Errorf("store: FindOutboundByShortRef: %w", ErrNotFound)
	}
	ref = ref[:8]
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, send_started_at, sent_at,
			attempts, next_attempt_at, delivery_error, meta
		FROM chat_messages
		WHERE org_id = $1 AND channel = $2 AND direction = 'out'
		  AND replace(id::text, '-', '') LIKE $3
		ORDER BY created_at DESC
		LIMIT 2`, orgID, channel, ref+"%")
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: FindOutboundByShortRef: %w", err)
	}
	out, err := scanChatMessages(rows)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("store: FindOutboundByShortRef: %w", err)
	}
	if len(out) != 1 {
		return ChatMessage{}, fmt.Errorf("store: FindOutboundByShortRef: %w", ErrNotFound)
	}
	return out[0], nil
}

// GetChatMessage returns a message by id (server role; optional org filter).
func (s *Store) GetChatMessage(ctx context.Context, id uuid.UUID) (ChatMessage, error) {
	var m ChatMessage
	var actions, evidence, meta []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, channel, account_id, direction, kind, body, actions, evidence,
			incident_id, created_at, delivered_at, send_started_at, sent_at,
			attempts, next_attempt_at, delivery_error, meta
		FROM chat_messages WHERE id = $1`, id,
	).Scan(
		&m.ID, &m.OrgID, &m.Channel, &m.AccountID, &m.Direction, &m.Kind, &m.Body,
		&actions, &evidence, &m.IncidentID, &m.CreatedAt, &m.DeliveredAt,
		&m.SendStartedAt, &m.SentAt,
		&m.Attempts, &m.NextAttemptAt, &m.DeliveryError, &meta,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChatMessage{}, fmt.Errorf("store: GetChatMessage: %w", ErrNotFound)
		}
		return ChatMessage{}, fmt.Errorf("store: GetChatMessage: %w", err)
	}
	if err := unmarshalChatJSON(actions, evidence, &m); err != nil {
		return ChatMessage{}, err
	}
	if err := unmarshalChatMeta(meta, &m); err != nil {
		return ChatMessage{}, err
	}
	return m, nil
}

// UserRole returns the role of a user in an organisation.
func (s *Store) UserRole(ctx context.Context, orgID, userID uuid.UUID) (string, error) {
	var role string
	err := s.pool.QueryRow(ctx, `
		SELECT role FROM users WHERE org_id = $1 AND id = $2 AND disabled_at IS NULL`,
		orgID, userID).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("store: UserRole: %w", ErrNotFound)
		}
		return "", fmt.Errorf("store: UserRole: %w", err)
	}
	return role, nil
}

// InsertIncidentForTest inserts an incident row (tests and chat action setup).
func (s *Store) InsertIncidentForTest(ctx context.Context, inc Incident) (Incident, error) {
	if inc.ID == uuid.Nil {
		inc.ID = uuid.New()
	}
	if inc.OpenedAt.IsZero() {
		inc.OpenedAt = time.Now().UTC()
	}
	if inc.UpdatedAt.IsZero() {
		inc.UpdatedAt = inc.OpenedAt
	}
	if inc.State == "" {
		inc.State = "open"
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO incidents (id, org_id, host_id, title, severity, state, opened_at, updated_at, resolved_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, org_id, host_id, title, severity, state, opened_at, updated_at, resolved_at`,
		inc.ID, inc.OrgID, inc.HostID, inc.Title, inc.Severity, inc.State,
		inc.OpenedAt, inc.UpdatedAt, inc.ResolvedAt,
	).Scan(&inc.ID, &inc.OrgID, &inc.HostID, &inc.Title, &inc.Severity, &inc.State,
		&inc.OpenedAt, &inc.UpdatedAt, &inc.ResolvedAt)
	if err != nil {
		return Incident{}, fmt.Errorf("store: InsertIncidentForTest: %w", err)
	}
	return inc, nil
}

// InsertAlertForTest inserts an alert row for tests.
func (s *Store) InsertAlertForTest(ctx context.Context, a Alert) (Alert, error) {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	if a.FirstAt.IsZero() {
		a.FirstAt = time.Now().UTC()
	}
	if a.LastAt.IsZero() {
		a.LastAt = a.FirstAt
	}
	if a.State == "" {
		a.State = "firing"
	}
	if a.Evidence == nil {
		a.Evidence = json.RawMessage(`{}`)
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO alerts (
			id, org_id, host_id, incident_id, rule, container, severity, state,
			dedupe_key, summary, evidence, first_at, last_at, resolved_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id, org_id, host_id, incident_id, rule, container, severity, state,
			dedupe_key, summary, evidence, first_at, last_at, resolved_at`,
		a.ID, a.OrgID, a.HostID, a.IncidentID, a.Rule, a.Container, a.Severity, a.State,
		a.DedupeKey, a.Summary, a.Evidence, a.FirstAt, a.LastAt, a.ResolvedAt,
	).Scan(
		&a.ID, &a.OrgID, &a.HostID, &a.IncidentID, &a.Rule, &a.Container,
		&a.Severity, &a.State, &a.DedupeKey, &a.Summary, &a.Evidence,
		&a.FirstAt, &a.LastAt, &a.ResolvedAt,
	)
	if err != nil {
		return Alert{}, fmt.Errorf("store: InsertAlertForTest: %w", err)
	}
	return a, nil
}

func newLinkCode(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	// Rejection sampling over a 32-symbol alphabet (256 % 32 == 0, so every
	// byte is usable; keep the same form as auth.NewCode for consistency).
	alphabetLen := len(linkCodeAlphabet)
	limit := 256 - (256 % alphabetLen)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("store: link code: %w", err)
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < n {
				out = append(out, linkCodeAlphabet[int(b)%alphabetLen])
			}
		}
	}
	return string(out), nil
}

func scanChatMessages(rows pgx.Rows) ([]ChatMessage, error) {
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var actions, evidence, meta []byte
		if err := rows.Scan(
			&m.ID, &m.OrgID, &m.Channel, &m.AccountID, &m.Direction, &m.Kind, &m.Body,
			&actions, &evidence, &m.IncidentID, &m.CreatedAt, &m.DeliveredAt,
			&m.SendStartedAt, &m.SentAt,
			&m.Attempts, &m.NextAttemptAt, &m.DeliveryError, &meta,
		); err != nil {
			return nil, err
		}
		if err := unmarshalChatJSON(actions, evidence, &m); err != nil {
			return nil, err
		}
		if err := unmarshalChatMeta(meta, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func unmarshalChatJSON(actions, evidence []byte, m *ChatMessage) error {
	if len(actions) == 0 {
		m.Actions = nil
	} else if err := json.Unmarshal(actions, &m.Actions); err != nil {
		return fmt.Errorf("store: chat actions: %w", err)
	}
	if len(evidence) == 0 {
		m.Evidence = nil
	} else if err := json.Unmarshal(evidence, &m.Evidence); err != nil {
		return fmt.Errorf("store: chat evidence: %w", err)
	}
	return nil
}

// SignActionToken returns an opaque HMAC-SHA256 token binding message id,
// action and expiry. secret must be at least 32 bytes in production.
func SignActionToken(secret []byte, messageID uuid.UUID, action string, expiry time.Time) string {
	payload := fmt.Sprintf("%s|%s|%d", messageID.String(), action, expiry.Unix())
	mac := hmacSHA256(secret, []byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac)
}

// VerifyActionToken checks an action token. Returns the message id and action
// when valid; uses constant-time comparison for the MAC.
func VerifyActionToken(secret []byte, token string, now time.Time) (messageID uuid.UUID, action string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return uuid.Nil, "", ErrActionRejected
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return uuid.Nil, "", ErrActionRejected
	}
	wantMAC, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return uuid.Nil, "", ErrActionRejected
	}
	gotMAC := hmacSHA256(secret, payload)
	// Compare fixed-size digests so length mismatch does not early-exit.
	sumGot := sha256.Sum256(gotMAC)
	sumWant := sha256.Sum256(wantMAC)
	if !hmac.Equal(sumGot[:], sumWant[:]) {
		return uuid.Nil, "", ErrActionRejected
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 3 {
		return uuid.Nil, "", ErrActionRejected
	}
	messageID, err = uuid.Parse(fields[0])
	if err != nil {
		return uuid.Nil, "", ErrActionRejected
	}
	action = fields[1]
	var exp int64
	if _, err := fmt.Sscanf(fields[2], "%d", &exp); err != nil {
		return uuid.Nil, "", ErrActionRejected
	}
	if now.Unix() > exp {
		return uuid.Nil, "", ErrActionRejected
	}
	switch action {
	case "later", "mute30d", "ack", ActionApprove, ActionDecline:
	default:
		return uuid.Nil, "", ErrActionRejected
	}
	return messageID, action, nil
}

func hmacSHA256(secret, msg []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(msg)
	return mac.Sum(nil)
}

func marshalChatMeta(m *ChatMeta) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func unmarshalChatMeta(raw []byte, m *ChatMessage) error {
	m.Meta = nil
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return nil
	}
	var meta ChatMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("store: chat meta: %w", err)
	}
	m.Meta = &meta
	return nil
}
