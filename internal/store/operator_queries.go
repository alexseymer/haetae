package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ContainerByID returns the current (not removed) container with the given
// container id in the organisation, or ErrNotFound (also for another
// organisation's container).
func (s *Store) ContainerByID(ctx context.Context, orgID uuid.UUID, containerID string) (Container, error) {
	c, err := scanContainer(s.pool.QueryRow(ctx, `
		SELECT org_id, host_id, container_id, name, stack, service, image, image_version,
			state, status, health, restart_count, oom_killed, exit_code, ports,
			created_at, started_at, updated_at, removed_at
		FROM containers
		WHERE org_id = $1 AND container_id = $2 AND removed_at IS NULL
		ORDER BY updated_at DESC LIMIT 1`, orgID, containerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Container{}, ErrNotFound
	}
	if err != nil {
		return Container{}, fmt.Errorf("store: ContainerByID: %w", err)
	}
	return c, nil
}

// ListDeploysForHost returns the newest deploys on a host (limit capped at 100).
func (s *Store) ListDeploysForHost(ctx context.Context, orgID, hostID uuid.UUID, limit int) ([]Deploy, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+deployCols+` FROM deploys
		WHERE org_id = $1 AND host_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, orgID, hostID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: ListDeploysForHost: %w", err)
	}
	defer rows.Close()
	return collectDeploys(rows)
}
