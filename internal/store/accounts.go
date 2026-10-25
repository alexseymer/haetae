package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Account errors.
var (
	// ErrEmailTaken is returned when creating a user whose email exists.
	ErrEmailTaken = errors.New("store: email already in use")
	// ErrAlreadySetUp is returned by SetupFirstUser once any user exists.
	ErrAlreadySetUp = errors.New("store: already set up")
)

const setupLockKey int64 = 7245

// User is a person who can sign in.
type User struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	Email        string
	PasswordHash string
	Role         string // "admin" or "member"
	CreatedAt    time.Time
	DisabledAt   *time.Time
}

// Session is a signed-in web session joined with its user.
type Session struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	OrgID      uuid.UUID
	Email      string
	Role       string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// userCols includes the password hash and is only for the server's own role
// (sign-in); publicUserCols is what the restricted role may read.
const (
	userCols       = `id, org_id, email, password_hash, role, created_at, disabled_at`
	publicUserCols = `id, org_id, email, role, created_at, disabled_at`
)

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.DisabledAt)
	return u, err
}

func scanPublicUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt, &u.DisabledAt)
	return u, err
}

// CreateUser adds a user to an organisation. passwordHash is an already
// hashed password (see internal/auth).
//
// The returned User has no PasswordHash: it works through the restricted role,
// which may not read it.
func (s *Store) CreateUser(ctx context.Context, orgID uuid.UUID, email, passwordHash, role string) (User, error) {
	u, err := scanPublicUser(s.pool.QueryRow(ctx, `
		INSERT INTO users (org_id, email, password_hash, role) VALUES ($1,$2,$3,$4)
		RETURNING `+publicUserCols, orgID, normalizeEmail(email), passwordHash, role))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, fmt.Errorf("store: CreateUser: %w", ErrEmailTaken)
		}
		return User{}, fmt.Errorf("store: CreateUser: %w", err)
	}
	return u, nil
}

// SetupFirstUser creates the first administrator in the default organisation,
// naming the organisation. It succeeds once: after any user exists it returns
// ErrAlreadySetUp. Safe under concurrency.
func (s *Store) SetupFirstUser(ctx context.Context, orgName, email, passwordHash string) (User, Org, error) {
	org, err := s.DefaultOrg(ctx)
	if err != nil {
		return User{}, Org{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, Org{}, fmt.Errorf("store: SetupFirstUser: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, setupLockKey); err != nil {
		return User{}, Org{}, fmt.Errorf("store: SetupFirstUser: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return User{}, Org{}, fmt.Errorf("store: SetupFirstUser: %w", err)
	}
	if n > 0 {
		return User{}, Org{}, ErrAlreadySetUp
	}
	if name := strings.TrimSpace(orgName); name != "" {
		if err := tx.QueryRow(ctx, `UPDATE orgs SET name = $2 WHERE id = $1 RETURNING name`, org.ID, name).Scan(&org.Name); err != nil {
			return User{}, Org{}, fmt.Errorf("store: SetupFirstUser: %w", err)
		}
	}
	u, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO users (org_id, email, password_hash, role) VALUES ($1,$2,$3,'admin')
		RETURNING `+userCols, org.ID, normalizeEmail(email), passwordHash))
	if err != nil {
		return User{}, Org{}, fmt.Errorf("store: SetupFirstUser: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, Org{}, fmt.Errorf("store: SetupFirstUser: %w", err)
	}
	return u, org, nil
}

// SetupRequired reports whether no user exists yet.
func (s *Store) SetupRequired(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: SetupRequired: %w", err)
	}
	return !exists, nil
}

// UserByEmail looks a user up for sign-in. Unscoped: the organisation is not
// known before the user is. Returns ErrNotFound if there is none.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, normalizeEmail(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, fmt.Errorf("store: UserByEmail: %w", ErrNotFound)
		}
		return User{}, fmt.Errorf("store: UserByEmail: %w", err)
	}
	return u, nil
}

// ListUsers returns the organisation's users by email, without password hashes.
func (s *Store) ListUsers(ctx context.Context, orgID uuid.UUID) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+publicUserCols+` FROM users WHERE org_id = $1 ORDER BY email`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: ListUsers: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanPublicUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ListUsers: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetPassword replaces a user's password hash. It needs the server's own role
// (the restricted role cannot update users); the caller has checked who is asking.
func (s *Store) SetPassword(ctx context.Context, orgID, userID uuid.UUID, passwordHash string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $3 WHERE org_id = $1 AND id = $2`, orgID, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("store: SetPassword: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: SetPassword: %w", ErrNotFound)
	}
	return nil
}

// CreateSession stores a session for a token hash. Unscoped (sign-in).
func (s *Store) CreateSession(ctx context.Context, userID, orgID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, org_id, expires_at) VALUES ($1,$2,$3,$4)`,
		tokenHash, userID, orgID, expiresAt)
	if err != nil {
		return fmt.Errorf("store: CreateSession: %w", err)
	}
	return nil
}

// SessionByTokenHash returns the live session for a token hash, or ErrNotFound
// when there is none, it expired, or its user is disabled.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash []byte, now time.Time) (Session, error) {
	var x Session
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.org_id, u.email, u.role, s.created_at, s.last_seen_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2 AND u.disabled_at IS NULL`, tokenHash, now,
	).Scan(&x.ID, &x.UserID, &x.OrgID, &x.Email, &x.Role, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, fmt.Errorf("store: SessionByTokenHash: %w", ErrNotFound)
		}
		return Session{}, fmt.Errorf("store: SessionByTokenHash: %w", err)
	}
	return x, nil
}

// TouchSession extends a session's idle timer.
func (s *Store) TouchSession(ctx context.Context, id uuid.UUID, now, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2, expires_at = $3 WHERE id = $1`, id, now, expiresAt); err != nil {
		return fmt.Errorf("store: TouchSession: %w", err)
	}
	return nil
}

// DeleteSession ends the session with this token hash.
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("store: DeleteSession: %w", err)
	}
	return nil
}

// DeleteUserSessions ends all of a user's sessions, optionally keeping one.
func (s *Store) DeleteUserSessions(ctx context.Context, userID uuid.UUID, keep []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND ($2::bytea IS NULL OR token_hash <> $2)`, userID, keep); err != nil {
		return fmt.Errorf("store: DeleteUserSessions: %w", err)
	}
	return nil
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions(ctx context.Context, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, now); err != nil {
		return fmt.Errorf("store: PruneSessions: %w", err)
	}
	return nil
}

// GetOrg returns an organisation by id, or ErrNotFound (also when row-level
// security hides it).
func (s *Store) GetOrg(ctx context.Context, id uuid.UUID) (Org, error) {
	var o Org
	err := s.pool.QueryRow(ctx, `SELECT id, name, created_at FROM orgs WHERE id = $1`, id).Scan(&o.ID, &o.Name, &o.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Org{}, fmt.Errorf("store: GetOrg: %w", ErrNotFound)
		}
		return Org{}, fmt.Errorf("store: GetOrg: %w", err)
	}
	return o, nil
}
