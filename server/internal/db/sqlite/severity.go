package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateSeverity inserts a new severity row.
func (s *Service) CreateSeverity(ctx context.Context, severity *models.Severity) error {
	if severity.Channels == "" {
		severity.Channels = "[]"
	}
	_, err := s.db.NewInsert().Model(severity).Exec(ctx)

	return err
}

// GetSeverity fetches a live severity by uid OR slug, scoped to the org.
func (s *Service) GetSeverity(
	ctx context.Context, orgUID, identifier string,
) (*models.Severity, error) {
	var sev models.Severity
	err := s.db.NewSelect().
		Model(&sev).
		Where("organization_uid = ?", orgUID).
		Where("(uid = ? OR slug = ?)", identifier, identifier).
		Where("deleted_at IS NULL").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return &sev, nil
}

// ListSeverities returns every live severity in the org.
func (s *Service) ListSeverities(
	ctx context.Context, filter *models.ListSeveritiesFilter,
) ([]*models.Severity, error) {
	var rows []*models.Severity
	query := s.db.NewSelect().Model(&rows).Where("deleted_at IS NULL")
	if filter != nil && filter.OrganizationUID != "" {
		query = query.Where("organization_uid = ?", filter.OrganizationUID)
	}
	query = query.Order("is_default DESC", "slug ASC")

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return rows, nil
}

// UpdateSeverity applies a partial update.
func (s *Service) UpdateSeverity(
	ctx context.Context, uid string, update *models.SeverityUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.Severity)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}
	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}
	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}
	if update.Channels != nil {
		encoded, err := json.Marshal(*update.Channels)
		if err != nil {
			return fmt.Errorf("failed to encode channels: %w", err)
		}
		query = query.Set("channels = ?", string(encoded))
	}
	switch {
	case update.ClearDefault:
		query = query.Set("is_default = 0")
	case update.IsDefault != nil:
		query = query.Set("is_default = ?", *update.IsDefault)
	}

	_, err := query.Exec(ctx)

	return err
}

// DeleteSeverity soft-deletes the row.
func (s *Service) DeleteSeverity(ctx context.Context, uid string) error {
	now := time.Now()
	_, err := s.db.NewUpdate().
		Model((*models.Severity)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Exec(ctx)

	return err
}

// ClearOrgDefaultSeverity demotes the current default severity in the
// org, if any. Called right before promoting a different severity so the
// `severities_org_default_alive_idx` partial unique index stays
// satisfied during the transition.
func (s *Service) ClearOrgDefaultSeverity(ctx context.Context, orgUID string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Severity)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("is_default = 1").
		Where("deleted_at IS NULL").
		Set("is_default = 0").
		Set("updated_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// GetOrgDefaultSeverity returns the org's current default severity.
func (s *Service) GetOrgDefaultSeverity(
	ctx context.Context, orgUID string,
) (*models.Severity, error) {
	var sev models.Severity
	err := s.db.NewSelect().
		Model(&sev).
		Where("organization_uid = ?", orgUID).
		Where("is_default = 1").
		Where("deleted_at IS NULL").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return &sev, nil
}
