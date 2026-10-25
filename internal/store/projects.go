package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNameTaken is returned when a project name already exists on the host.
var ErrNameTaken = errors.New("store: project name already in use")

// Project is a deployable app on one host.
type Project struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	HostID         uuid.UUID
	Name           string
	SourceKind     string // "git" or "inline"
	GitURL         string
	GitRef         string
	ComposePath    string
	GitTokenSealed []byte // set only when read with the server role
	HasToken       bool
	InlineCompose  string
	Domain         string
	Service        string
	Port           int
	AutoDeploy     bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ProjectInput is the writable fields for CreateProject / UpdateProject.
type ProjectInput struct {
	Name           string
	SourceKind     string
	GitURL         string
	GitRef         string
	ComposePath    string
	GitTokenSealed []byte // nil = no token; non-nil (possibly empty) sealed blob
	ClearToken     bool   // UpdateProject: remove the token
	SetToken       bool   // UpdateProject: replace with GitTokenSealed
	InlineCompose  string
	Domain         string
	Service        string
	Port           int
	AutoDeploy     bool
}

const projectPublicCols = `
	id, org_id, host_id, name, source_kind, git_url, git_ref, compose_path,
	has_git_token, inline_compose, domain, service, port, auto_deploy,
	created_at, updated_at`

const projectServerCols = `
	id, org_id, host_id, name, source_kind, git_url, git_ref, compose_path,
	git_token_sealed, has_git_token, inline_compose, domain, service, port, auto_deploy,
	created_at, updated_at`

func scanProjectPublic(row scannable) (Project, error) {
	var p Project
	err := row.Scan(
		&p.ID, &p.OrgID, &p.HostID, &p.Name, &p.SourceKind, &p.GitURL, &p.GitRef, &p.ComposePath,
		&p.HasToken, &p.InlineCompose, &p.Domain, &p.Service, &p.Port, &p.AutoDeploy,
		&p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

func scanProjectServer(row scannable) (Project, error) {
	var p Project
	err := row.Scan(
		&p.ID, &p.OrgID, &p.HostID, &p.Name, &p.SourceKind, &p.GitURL, &p.GitRef, &p.ComposePath,
		&p.GitTokenSealed, &p.HasToken, &p.InlineCompose, &p.Domain, &p.Service, &p.Port, &p.AutoDeploy,
		&p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

// CreateProject inserts a project. id may be uuid.Nil to let the database
// assign one; otherwise the given id is used (needed so a token can be sealed
// with AAD = project id before insert).
func (s *Store) CreateProject(ctx context.Context, orgID, hostID uuid.UUID, id uuid.UUID, in ProjectInput) (Project, error) {
	if id == uuid.Nil {
		var err error
		id, err = uuid.NewRandom()
		if err != nil {
			return Project{}, fmt.Errorf("store: CreateProject: %w", err)
		}
	}
	var sealed any
	if in.GitTokenSealed != nil {
		sealed = in.GitTokenSealed
	}
	p, err := scanProjectPublic(s.pool.QueryRow(ctx, `
		INSERT INTO projects (
			id, org_id, host_id, name, source_kind, git_url, git_ref, compose_path,
			git_token_sealed, inline_compose, domain, service, port, auto_deploy
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+projectPublicCols,
		id, orgID, hostID, in.Name, in.SourceKind, in.GitURL, in.GitRef, in.ComposePath,
		sealed, in.InlineCompose, in.Domain, in.Service, in.Port, in.AutoDeploy,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Project{}, fmt.Errorf("store: CreateProject: %w", ErrNameTaken)
		}
		return Project{}, fmt.Errorf("store: CreateProject: %w", err)
	}
	return p, nil
}

// GetProject returns a project in orgID. The sealed token is only populated
// when running as the server role (unscoped store).
func (s *Store) GetProject(ctx context.Context, orgID, id uuid.UUID) (Project, error) {
	if s.scoped {
		p, err := scanProjectPublic(s.pool.QueryRow(ctx,
			`SELECT `+projectPublicCols+` FROM projects WHERE org_id = $1 AND id = $2`, orgID, id))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Project{}, fmt.Errorf("store: GetProject: %w", ErrNotFound)
			}
			return Project{}, fmt.Errorf("store: GetProject: %w", err)
		}
		return p, nil
	}
	p, err := scanProjectServer(s.pool.QueryRow(ctx,
		`SELECT `+projectServerCols+` FROM projects WHERE org_id = $1 AND id = $2`, orgID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Project{}, fmt.Errorf("store: GetProject: %w", ErrNotFound)
		}
		return Project{}, fmt.Errorf("store: GetProject: %w", err)
	}
	return p, nil
}

// GetProjectByName returns the project named name on hostID in orgID.
func (s *Store) GetProjectByName(ctx context.Context, orgID, hostID uuid.UUID, name string) (Project, error) {
	if s.scoped {
		p, err := scanProjectPublic(s.pool.QueryRow(ctx,
			`SELECT `+projectPublicCols+` FROM projects WHERE org_id = $1 AND host_id = $2 AND name = $3`,
			orgID, hostID, name))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Project{}, fmt.Errorf("store: GetProjectByName: %w", ErrNotFound)
			}
			return Project{}, fmt.Errorf("store: GetProjectByName: %w", err)
		}
		return p, nil
	}
	p, err := scanProjectServer(s.pool.QueryRow(ctx,
		`SELECT `+projectServerCols+` FROM projects WHERE org_id = $1 AND host_id = $2 AND name = $3`,
		orgID, hostID, name))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Project{}, fmt.Errorf("store: GetProjectByName: %w", ErrNotFound)
		}
		return Project{}, fmt.Errorf("store: GetProjectByName: %w", err)
	}
	return p, nil
}

// ListProjects returns projects on one host, ordered by name.
func (s *Store) ListProjects(ctx context.Context, orgID, hostID uuid.UUID) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectPublicCols+` FROM projects WHERE org_id = $1 AND host_id = $2 ORDER BY name`,
		orgID, hostID)
	if err != nil {
		return nil, fmt.Errorf("store: ListProjects: %w", err)
	}
	defer rows.Close()
	return collectProjects(rows)
}

// ListAllProjects returns every project in the organisation, ordered by name.
func (s *Store) ListAllProjects(ctx context.Context, orgID uuid.UUID) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectPublicCols+` FROM projects WHERE org_id = $1 ORDER BY name, id`,
		orgID)
	if err != nil {
		return nil, fmt.Errorf("store: ListAllProjects: %w", err)
	}
	defer rows.Close()
	return collectProjects(rows)
}

func collectProjects(rows pgx.Rows) ([]Project, error) {
	var out []Project
	for rows.Next() {
		p, err := scanProjectPublic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProject applies a partial update. Only fields the caller sets via the
// input flags (SetToken/ClearToken) and non-zero conventions are written; the
// caller passes the full desired public fields after merging.
func (s *Store) UpdateProject(ctx context.Context, orgID, id uuid.UUID, in ProjectInput) (Project, error) {
	var (
		sealed any
		setTok bool
	)
	switch {
	case in.ClearToken:
		sealed = nil
		setTok = true
	case in.SetToken:
		sealed = in.GitTokenSealed
		setTok = true
	}

	var p Project
	var err error
	if setTok {
		p, err = scanProjectPublic(s.pool.QueryRow(ctx, `
			UPDATE projects SET
				git_url = $3, git_ref = $4, compose_path = $5,
				git_token_sealed = $6, inline_compose = $7,
				domain = $8, service = $9, port = $10, auto_deploy = $11,
				updated_at = now()
			WHERE org_id = $1 AND id = $2
			RETURNING `+projectPublicCols,
			orgID, id, in.GitURL, in.GitRef, in.ComposePath, sealed, in.InlineCompose,
			in.Domain, in.Service, in.Port, in.AutoDeploy,
		))
	} else {
		p, err = scanProjectPublic(s.pool.QueryRow(ctx, `
			UPDATE projects SET
				git_url = $3, git_ref = $4, compose_path = $5,
				inline_compose = $6,
				domain = $7, service = $8, port = $9, auto_deploy = $10,
				updated_at = now()
			WHERE org_id = $1 AND id = $2
			RETURNING `+projectPublicCols,
			orgID, id, in.GitURL, in.GitRef, in.ComposePath, in.InlineCompose,
			in.Domain, in.Service, in.Port, in.AutoDeploy,
		))
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Project{}, fmt.Errorf("store: UpdateProject: %w", ErrNotFound)
		}
		return Project{}, fmt.Errorf("store: UpdateProject: %w", err)
	}
	return p, nil
}

// DeleteProject removes a project and its deploys/logs. Server role only.
func (s *Store) DeleteProject(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("store: DeleteProject: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: DeleteProject: %w", ErrNotFound)
	}
	return nil
}
