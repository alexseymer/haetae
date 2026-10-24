package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrTokenInvalid is returned for unknown, used and expired tokens alike,
// so callers cannot tell them apart.
var ErrTokenInvalid = errors.New("store: enrollment token invalid")

// ErrHostNameTaken is returned when enrolling a host whose name already exists in the org.
var ErrHostNameTaken = errors.New("store: host name already in use")

const defaultOrgLockKey int64 = 7244

// Org is a tenant organization.
type Org struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
}

// HostInfo holds runtime identity reported by an agent.
type HostInfo struct {
	Hostname      string
	OS            string
	Kernel        string
	Arch          string
	DockerVersion string
	AgentVersion  string
}

// Host is an enrolled agent host.
type Host struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Name       string
	Info       HostInfo
	CertSerial string
	EnrolledAt time.Time
	LastSeenAt *time.Time
	Connected  bool
	RevokedAt  *time.Time
}

// EnrollmentToken is the metadata for a one-time enrollment token (plaintext is not stored).
type EnrollmentToken struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	HostName  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateOrg inserts a new organization with the given name.
func (s *Store) CreateOrg(ctx context.Context, name string) (Org, error) {
	var o Org
	err := s.pool.QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ($1) RETURNING id, name, created_at`,
		name,
	).Scan(&o.ID, &o.Name, &o.CreatedAt)
	if err != nil {
		return Org{}, fmt.Errorf("store: CreateOrg: %w", err)
	}
	return o, nil
}

// DefaultOrg returns the oldest org, creating one named "default" if none exists.
// Concurrent callers are safe via an advisory lock held inside the transaction.
func (s *Store) DefaultOrg(ctx context.Context) (Org, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Org{}, fmt.Errorf("store: DefaultOrg: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, defaultOrgLockKey); err != nil {
		return Org{}, fmt.Errorf("store: DefaultOrg: %w", err)
	}

	var o Org
	err = tx.QueryRow(ctx,
		`SELECT id, name, created_at FROM orgs ORDER BY created_at ASC, id ASC LIMIT 1`,
	).Scan(&o.ID, &o.Name, &o.CreatedAt)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Org{}, fmt.Errorf("store: DefaultOrg: %w", err)
		}
		return o, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Org{}, fmt.Errorf("store: DefaultOrg: %w", err)
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ('default') RETURNING id, name, created_at`,
	).Scan(&o.ID, &o.Name, &o.CreatedAt)
	if err != nil {
		return Org{}, fmt.Errorf("store: DefaultOrg: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Org{}, fmt.Errorf("store: DefaultOrg: %w", err)
	}
	return o, nil
}

// CreateEnrollmentToken creates a one-time enrollment token for hostName.
// Only the SHA-256 of the token is stored; the plaintext is returned once.
func (s *Store) CreateEnrollmentToken(ctx context.Context, orgID uuid.UUID, hostName string, ttl time.Duration) (string, EnrollmentToken, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", EnrollmentToken{}, fmt.Errorf("store: CreateEnrollmentToken: %w", err)
	}
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
	plaintext := "T-" + encoded
	sum := sha256.Sum256([]byte(plaintext))

	expiresAt := time.Now().UTC().Add(ttl)
	var tok EnrollmentToken
	err := s.pool.QueryRow(ctx, `
		INSERT INTO enrollment_tokens (org_id, token_hash, host_name, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, org_id, host_name, created_at, expires_at`,
		orgID, sum[:], hostName, expiresAt,
	).Scan(&tok.ID, &tok.OrgID, &tok.HostName, &tok.CreatedAt, &tok.ExpiresAt)
	if err != nil {
		return "", EnrollmentToken{}, fmt.Errorf("store: CreateEnrollmentToken: %w", err)
	}
	return plaintext, tok, nil
}

// EnrollHost atomically consumes an enrollment token and creates a host.
// issue is called with the new host ID to obtain a certificate serial; on error the transaction rolls back.
func (s *Store) EnrollHost(ctx context.Context, token string, info HostInfo, issue func(hostID uuid.UUID) (serial string, err error)) (Host, error) {
	sum := sha256.Sum256([]byte(token))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Host{}, fmt.Errorf("store: EnrollHost: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		tokID    uuid.UUID
		orgID    uuid.UUID
		hostName string
	)
	err = tx.QueryRow(ctx, `
		SELECT id, org_id, host_name
		FROM enrollment_tokens
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		FOR UPDATE`,
		sum[:],
	).Scan(&tokID, &orgID, &hostName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Host{}, fmt.Errorf("store: EnrollHost: %w", ErrTokenInvalid)
		}
		return Host{}, fmt.Errorf("store: EnrollHost: %w", err)
	}

	hostID, err := uuid.NewRandom()
	if err != nil {
		return Host{}, fmt.Errorf("store: EnrollHost: %w", err)
	}

	serial, err := issue(hostID)
	if err != nil {
		return Host{}, fmt.Errorf("store: EnrollHost: %w", err)
	}

	var h Host
	err = tx.QueryRow(ctx, `
		INSERT INTO hosts (
			id, org_id, name, hostname, os, kernel, arch, docker_version, agent_version, cert_serial
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, org_id, name, hostname, os, kernel, arch, docker_version, agent_version,
			cert_serial, enrolled_at, last_seen_at, connected, revoked_at`,
		hostID, orgID, hostName,
		info.Hostname, info.OS, info.Kernel, info.Arch, info.DockerVersion, info.AgentVersion,
		serial,
	).Scan(
		&h.ID, &h.OrgID, &h.Name,
		&h.Info.Hostname, &h.Info.OS, &h.Info.Kernel, &h.Info.Arch, &h.Info.DockerVersion, &h.Info.AgentVersion,
		&h.CertSerial, &h.EnrolledAt, &h.LastSeenAt, &h.Connected, &h.RevokedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Host{}, fmt.Errorf("store: EnrollHost: %w", ErrHostNameTaken)
		}
		return Host{}, fmt.Errorf("store: EnrollHost: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE enrollment_tokens SET used_at = now(), host_id = $1 WHERE id = $2`,
		h.ID, tokID,
	)
	if err != nil {
		return Host{}, fmt.Errorf("store: EnrollHost: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Host{}, fmt.Errorf("store: EnrollHost: %w", err)
	}
	return h, nil
}

// GetHost returns the host with the given id, or ErrNotFound.
func (s *Store) GetHost(ctx context.Context, id uuid.UUID) (Host, error) {
	h, err := scanHost(s.pool.QueryRow(ctx, hostSelect+` WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Host{}, fmt.Errorf("store: GetHost: %w", ErrNotFound)
		}
		return Host{}, fmt.Errorf("store: GetHost: %w", err)
	}
	return h, nil
}

// ListHosts returns all hosts for orgID ordered by name.
func (s *Store) ListHosts(ctx context.Context, orgID uuid.UUID) ([]Host, error) {
	rows, err := s.pool.Query(ctx, hostSelect+` WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: ListHosts: %w", err)
	}
	defer rows.Close()

	var out []Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ListHosts: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListHosts: %w", err)
	}
	return out, nil
}

// MarkConnected sets connected=true, last_seen_at=at, and updates host info fields.
func (s *Store) MarkConnected(ctx context.Context, id uuid.UUID, info HostInfo, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE hosts SET
			connected = true,
			last_seen_at = $2,
			hostname = $3,
			os = $4,
			kernel = $5,
			arch = $6,
			docker_version = $7,
			agent_version = $8
		WHERE id = $1`,
		id, at,
		info.Hostname, info.OS, info.Kernel, info.Arch, info.DockerVersion, info.AgentVersion,
	)
	if err != nil {
		return fmt.Errorf("store: MarkConnected: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: MarkConnected: %w", ErrNotFound)
	}
	return nil
}

// MarkSeen updates last_seen_at for the host.
func (s *Store) MarkSeen(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE hosts SET last_seen_at = $2 WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("store: MarkSeen: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: MarkSeen: %w", ErrNotFound)
	}
	return nil
}

// MarkDisconnected sets connected=false for the host.
func (s *Store) MarkDisconnected(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `UPDATE hosts SET connected = false WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: MarkDisconnected: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: MarkDisconnected: %w", ErrNotFound)
	}
	return nil
}

// ResetConnections sets connected=false for all hosts; called at server start.
func (s *Store) ResetConnections(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET connected = false`)
	if err != nil {
		return fmt.Errorf("store: ResetConnections: %w", err)
	}
	return nil
}

const hostSelect = `
	SELECT id, org_id, name, hostname, os, kernel, arch, docker_version, agent_version,
		cert_serial, enrolled_at, last_seen_at, connected, revoked_at
	FROM hosts`

type scannable interface {
	Scan(dest ...any) error
}

func scanHost(row scannable) (Host, error) {
	var h Host
	err := row.Scan(
		&h.ID, &h.OrgID, &h.Name,
		&h.Info.Hostname, &h.Info.OS, &h.Info.Kernel, &h.Info.Arch, &h.Info.DockerVersion, &h.Info.AgentVersion,
		&h.CertSerial, &h.EnrolledAt, &h.LastSeenAt, &h.Connected, &h.RevokedAt,
	)
	return h, err
}
