package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
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
func (s *Service) CreateStatusUpdate(ctx context.Context, update *models.StatusUpdate) error {
	_, err := s.db.NewInsert().Model(update).Exec(ctx)

	return err
}

// GetStatusUpdateByUID retrieves a status update by UID.
func (s *Service) GetStatusUpdateByUID(ctx context.Context, uid string) (*models.StatusUpdate, error) {
	update := new(models.StatusUpdate)

	err := s.db.NewSelect().
		Model(update).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return update, nil
}

// UpdateStatusUpdate updates an existing status update row.
func (s *Service) UpdateStatusUpdate(ctx context.Context, update *models.StatusUpdate) error {
	update.UpdatedAt = time.Now()

	_, err := s.db.NewUpdate().
		Model(update).
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

// ListPublicStatusUpdates returns recent non-deleted status updates for a status page
// ordered by published_at DESC within the given history window (in days).
// Returns an empty slice when the table does not yet exist (graceful degradation).
func (s *Service) ListPublicStatusUpdates(
	ctx context.Context, statusPageUID string, historyDays int,
) ([]*db.PublicStatusUpdate, error) {
	if historyDays <= 0 {
		return []*db.PublicStatusUpdate{}, nil
	}

	rawQuery := fmt.Sprintf(
		`SELECT uid, section_uid, check_uid, incident_uid, title, body_markdown, link_url, kind, published_at
		 FROM status_updates
		 WHERE status_page_uid = $1
		   AND deleted_at IS NULL
		   AND published_at >= NOW() - INTERVAL '%d days'
		 ORDER BY published_at DESC
		 LIMIT 100`,
		historyDays,
	)

	rows, err := s.db.QueryContext(ctx, rawQuery, statusPageUID)
	if err != nil {
		// Graceful degradation: table may not exist yet.
		if strings.Contains(err.Error(), "does not exist") ||
			strings.Contains(err.Error(), "undefined_table") {
			return []*db.PublicStatusUpdate{}, nil
		}

		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var updates []*db.PublicStatusUpdate

	for rows.Next() {
		entry := &db.PublicStatusUpdate{}
		if scanErr := rows.Scan(
			&entry.UID, &entry.SectionUID, &entry.CheckUID, &entry.IncidentUID,
			&entry.Title, &entry.BodyMarkdown, &entry.LinkURL, &entry.Kind, &entry.PublishedAt,
		); scanErr != nil {
			return nil, scanErr
		}

		updates = append(updates, entry)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return updates, nil
}
