package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Port is a published container port binding.
type Port struct {
	IP      string `json:"ip"`
	Private uint16 `json:"private"`
	Public  uint16 `json:"public"`
	Proto   string `json:"proto"`
}

// Container is the current or last-known state of a container on a host.
type Container struct {
	OrgID        uuid.UUID
	HostID       uuid.UUID
	ContainerID  string
	Name         string
	Stack        string
	Service      string
	Image        string
	ImageVersion string
	State        string
	Status       string
	Health       string
	RestartCount int
	OOMKilled    bool
	ExitCode     int
	Ports        []Port
	CreatedAt    *time.Time
	StartedAt    *time.Time
	UpdatedAt    time.Time
	RemovedAt    *time.Time
}

// ReplaceContainers stores a full snapshot of a host's containers in one transaction: upserts every given container
// (removed_at = NULL, updated_at = now()) and sets removed_at = now() on rows of that host not in the snapshot and not
// already removed. orgID and hostID override the fields in cs.
func (s *Store) ReplaceContainers(ctx context.Context, orgID, hostID uuid.UUID, cs []Container) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: ReplaceContainers: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ports, err := json.Marshal(c.Ports)
		if err != nil {
			return fmt.Errorf("store: ReplaceContainers: %w", err)
		}
		if ports == nil {
			ports = []byte("[]")
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO containers (
				org_id, host_id, container_id, name, stack, service, image, image_version,
				state, status, health, restart_count, oom_killed, exit_code, ports,
				created_at, started_at, updated_at, removed_at
			) VALUES (
				$1,$2,$3,$4,$5,$6,$7,$8,
				$9,$10,$11,$12,$13,$14,$15,
				$16,$17,now(),NULL
			)
			ON CONFLICT (host_id, container_id) DO UPDATE SET
				org_id = EXCLUDED.org_id,
				name = EXCLUDED.name,
				stack = EXCLUDED.stack,
				service = EXCLUDED.service,
				image = EXCLUDED.image,
				image_version = EXCLUDED.image_version,
				state = EXCLUDED.state,
				status = EXCLUDED.status,
				health = EXCLUDED.health,
				restart_count = EXCLUDED.restart_count,
				oom_killed = EXCLUDED.oom_killed,
				exit_code = EXCLUDED.exit_code,
				ports = EXCLUDED.ports,
				created_at = EXCLUDED.created_at,
				started_at = EXCLUDED.started_at,
				updated_at = now(),
				removed_at = NULL`,
			orgID, hostID, c.ContainerID, c.Name, c.Stack, c.Service, c.Image, c.ImageVersion,
			c.State, c.Status, c.Health, c.RestartCount, c.OOMKilled, c.ExitCode, ports,
			c.CreatedAt, c.StartedAt,
		)
		if err != nil {
			return fmt.Errorf("store: ReplaceContainers: %w", err)
		}
		ids = append(ids, c.ContainerID)
	}

	if len(ids) == 0 {
		_, err = tx.Exec(ctx, `
			UPDATE containers
			SET removed_at = now(), updated_at = now()
			WHERE org_id = $1 AND host_id = $2 AND removed_at IS NULL`,
			orgID, hostID,
		)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE containers
			SET removed_at = now(), updated_at = now()
			WHERE org_id = $1 AND host_id = $2 AND removed_at IS NULL
			  AND NOT (container_id = ANY($3))`,
			orgID, hostID, ids,
		)
	}
	if err != nil {
		return fmt.Errorf("store: ReplaceContainers: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: ReplaceContainers: %w", err)
	}
	return nil
}

// ListContainers returns the host's current (not removed) containers ordered by stack, service, name. Scoped by org.
func (s *Store) ListContainers(ctx context.Context, orgID, hostID uuid.UUID) ([]Container, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, host_id, container_id, name, stack, service, image, image_version,
			state, status, health, restart_count, oom_killed, exit_code, ports,
			created_at, started_at, updated_at, removed_at
		FROM containers
		WHERE org_id = $1 AND host_id = $2 AND removed_at IS NULL
		ORDER BY stack, service, name`,
		orgID, hostID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: ListContainers: %w", err)
	}
	defer rows.Close()

	var out []Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ListContainers: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListContainers: %w", err)
	}
	return out, nil
}

func scanContainer(row scannable) (Container, error) {
	var (
		c     Container
		ports []byte
	)
	err := row.Scan(
		&c.OrgID, &c.HostID, &c.ContainerID, &c.Name, &c.Stack, &c.Service, &c.Image, &c.ImageVersion,
		&c.State, &c.Status, &c.Health, &c.RestartCount, &c.OOMKilled, &c.ExitCode, &ports,
		&c.CreatedAt, &c.StartedAt, &c.UpdatedAt, &c.RemovedAt,
	)
	if err != nil {
		return Container{}, err
	}
	if len(ports) > 0 {
		if err := json.Unmarshal(ports, &c.Ports); err != nil {
			return Container{}, err
		}
	}
	if c.Ports == nil {
		c.Ports = []Port{}
	}
	return c, nil
}
