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
		Where("su.organization_uid = ?", orgUID).
		Where("su.deleted_at IS NULL").
		OrderExpr("su.published_at DESC")

	if filter.StatusPageUID != "" {
		query = query.Where("su.status_page_uid = ?", filter.StatusPageUID)
	}

	if filter.SectionUID != nil {
		query = query.Where("su.section_uid = ?", *filter.SectionUID)
	}

	if filter.CheckUID != nil {
		query = query.Where("su.check_uid = ?", *filter.CheckUID)
	}

	if filter.IncidentUID != nil {
		query = query.Where("su.incident_uid = ?", *filter.IncidentUID)
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

// ListPublicStatusUpdates returns recent status updates for a status page within the given
// history window. Returns an empty slice when the status_updates table does not yet exist.
func (s *Service) ListPublicStatusUpdates(
	ctx context.Context, statusPageUID string, historyDays int,
) ([]*db.PublicStatusUpdate, error) {
	if historyDays <= 0 {
		return []*db.PublicStatusUpdate{}, nil
	}

	type row struct {
		UID          string    `bun:"uid"`
		SectionUID   *string   `bun:"section_uid"`
		CheckUID     *string   `bun:"check_uid"`
		IncidentUID  *string   `bun:"incident_uid"`
		Title        string    `bun:"title"`
		BodyMarkdown string    `bun:"body_markdown"`
		LinkURL      *string   `bun:"link_url"`
		Kind         string    `bun:"kind"`
		PublishedAt  time.Time `bun:"published_at"`
	}

	var rows []row

	query := fmt.Sprintf(
		`SELECT uid, section_uid, check_uid, incident_uid, title, body_markdown, link_url, kind, published_at
		 FROM status_updates
		 WHERE status_page_uid = ?
		   AND deleted_at IS NULL
		   AND published_at >= NOW() - INTERVAL '%d days'
		 ORDER BY published_at DESC
		 LIMIT 100`,
		historyDays,
	)

	err := s.db.NewRaw(query, statusPageUID).Scan(ctx, &rows)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") ||
			strings.Contains(err.Error(), "undefined_table") {
			return []*db.PublicStatusUpdate{}, nil
		}

		return nil, err
	}

	result := make([]*db.PublicStatusUpdate, len(rows))
	for i, r := range rows {
		result[i] = &db.PublicStatusUpdate{
			UID:          r.UID,
			SectionUID:   r.SectionUID,
			CheckUID:     r.CheckUID,
			IncidentUID:  r.IncidentUID,
			Title:        r.Title,
			BodyMarkdown: r.BodyMarkdown,
			LinkURL:      r.LinkURL,
			Kind:         r.Kind,
			PublishedAt:  r.PublishedAt,
		}
	}

	return result, nil
}
