package sqlite

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

	if filter.IncidentPublicationUID != nil {
		query = query.Where("incident_publication_uid = ?", *filter.IncidentPublicationUID)
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

// ListPublicStatusUpdates returns recent status updates for a status page within the given
// history window. Returns an empty slice when the status_updates table does not yet exist.
func (s *Service) ListPublicStatusUpdates(
	ctx context.Context, statusPageUID string, historyDays int,
) ([]*db.PublicStatusUpdate, error) {
	if historyDays <= 0 {
		return []*db.PublicStatusUpdate{}, nil
	}

	type rowResult struct {
		UID            string    `bun:"uid"`
		SectionUID     *string   `bun:"section_uid"`
		CheckUID       *string   `bun:"check_uid"`
		IncidentUID    *string   `bun:"incident_uid"`
		PublicationUID *string   `bun:"incident_publication_uid"`
		Title          string    `bun:"title"`
		BodyMarkdown   string    `bun:"body_markdown"`
		LinkURL        *string   `bun:"link_url"`
		Kind           string    `bun:"kind"`
		PublishedAt    time.Time `bun:"published_at"`
	}

	var rowResults []rowResult

	rawQuery := fmt.Sprintf(
		`SELECT uid, section_uid, check_uid, incident_uid, incident_publication_uid, title, body_markdown, link_url, kind, published_at
		 FROM status_updates
		 WHERE status_page_uid = ?
		   AND deleted_at IS NULL
		   AND published_at >= datetime('now', '-%d days')
		 ORDER BY published_at DESC
		 LIMIT 100`,
		historyDays,
	)

	err := s.db.NewRaw(rawQuery, statusPageUID).Scan(ctx, &rowResults)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return []*db.PublicStatusUpdate{}, nil
		}

		return nil, err
	}

	result := make([]*db.PublicStatusUpdate, len(rowResults))
	for idx := range rowResults {
		entry := &rowResults[idx]
		result[idx] = &db.PublicStatusUpdate{
			UID:            entry.UID,
			SectionUID:     entry.SectionUID,
			CheckUID:       entry.CheckUID,
			IncidentUID:    entry.IncidentUID,
			PublicationUID: entry.PublicationUID,
			Title:          entry.Title,
			BodyMarkdown:   entry.BodyMarkdown,
			LinkURL:        entry.LinkURL,
			Kind:           entry.Kind,
			PublishedAt:    entry.PublishedAt,
		}
	}

	return result, nil
}
