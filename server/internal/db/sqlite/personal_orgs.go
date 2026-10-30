package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// GetAutoPersonalOrgByOwner returns the auto-created personal organization of
// userUID (spec 2026-10-30-04), or nil when there is none. includeDeleted also
// considers soft-deleted ones, most recent first: a user gets one personal org
// in their lifetime, so one they deleted (or the cleanup removed) is never
// re-created behind their back.
func (s *Service) GetAutoPersonalOrgByOwner(
	ctx context.Context, userUID string, includeDeleted bool,
) (*models.Organization, error) {
	org := new(models.Organization)

	query := s.db.NewSelect().
		Model(org).
		Where("auto_personal_owner_uid = ?", userUID).
		Order("created_at DESC").
		Limit(1)

	if !includeDeleted {
		query = query.Where("deleted_at IS NULL")
	}

	if err := query.Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // "no personal org" is a legitimate answer.
		}

		return nil, err
	}

	return org, nil
}
