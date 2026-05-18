package postgres

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ListStatusUpdates returns status updates for an org, filtered and ordered by published_at DESC.
func (s *Service) ListStatusUpdates(
	ctx context.Context, orgUID string, filter models.StatusUpdatesFilter,
) ([]*models.StatusUpdate, error) {
	var updates []*models.StatusUpdate

	query := s.db.NewSelect().
		Model(&updates).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("published_at DESC")

	if filter.StatusPageUID != "" {
		query = query.Where("status_page_uid = ?", filter.StatusPageUID)
	}

	if filter.SectionUID != nil {
		query = query.Where("section_uid = ?", *filter.SectionUID)
	}

	if filter.CheckUID != nil {
		query = query.Where("check_uid = ?", *filter.CheckUID)
	}

	if filter.IncidentUID != nil {
		query = query.Where("incident_uid = ?", *filter.IncidentUID)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}

	query = query.Limit(limit)

	if filter.Offset > 0 {
		query = query.Offset(filter.Offset)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return updates, nil
}

// CreateStatusUpdate inserts a new status update.
func (s *Service) CreateStatusUpdate(ctx context.Context, su *models.StatusUpdate) error {
	_, err := s.db.NewInsert().Model(su).Exec(ctx)

	return err
}

// GetStatusUpdateByUID retrieves a status update by UID.
func (s *Service) GetStatusUpdateByUID(ctx context.Context, uid string) (*models.StatusUpdate, error) {
	su := new(models.StatusUpdate)

	err := s.db.NewSelect().
		Model(su).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return su, nil
}

// UpdateStatusUpdate updates an existing status update row.
func (s *Service) UpdateStatusUpdate(ctx context.Context, su *models.StatusUpdate) error {
	su.UpdatedAt = time.Now()

	_, err := s.db.NewUpdate().
		Model(su).
		WherePK().
		Exec(ctx)

	return err
}

// SoftDeleteStatusUpdate sets deleted_at on a status update.
func (s *Service) SoftDeleteStatusUpdate(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusUpdate)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Exec(ctx)

	return err
}
