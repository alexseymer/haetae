package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OrgAutonomy returns org_settings.autonomy: report, suggest or act. An
// organisation without a settings row is at the safe default, report.
func (s *Store) OrgAutonomy(ctx context.Context, orgID uuid.UUID) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT autonomy FROM org_settings WHERE org_id = $1`, orgID).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "report", nil
	}
	return v, err
}

// SetOrgAutonomy sets the autonomy level (report, suggest or act).
func (s *Store) SetOrgAutonomy(ctx context.Context, orgID uuid.UUID, level string) error {
	switch level {
	case "report", "suggest", "act":
	default:
		return errors.New("store: unknown autonomy level")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO org_settings (org_id, autonomy) VALUES ($1, $2)
		ON CONFLICT (org_id) DO UPDATE SET autonomy = EXCLUDED.autonomy, updated_at = now()`,
		orgID, level)
	return err
}
