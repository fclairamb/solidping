package postgres

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateIncidentPublication inserts a publication row. A duplicate
// (incident_uid, status_page_uid) is rejected by the partial unique index —
// callers recover by re-reading with FindIncidentPublication rather than
// minting a second public incident.
func (s *Service) CreateIncidentPublication(ctx context.Context, pub *models.IncidentPublication) error {
	_, err := s.db.NewInsert().Model(pub).Exec(ctx)

	return err
}

// GetIncidentPublication reads one live publication, scoped to the org.
func (s *Service) GetIncidentPublication(
	ctx context.Context, orgUID, uid string,
) (*models.IncidentPublication, error) {
	pub := new(models.IncidentPublication)

	err := s.db.NewSelect().
		Model(pub).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return pub, nil
}

// FindIncidentPublication returns the live publication for an (incident, page)
// pair, or sql.ErrNoRows when there is none.
func (s *Service) FindIncidentPublication(
	ctx context.Context, incidentUID, statusPageUID string,
) (*models.IncidentPublication, error) {
	pub := new(models.IncidentPublication)

	err := s.db.NewSelect().
		Model(pub).
		Where("incident_uid = ?", incidentUID).
		Where("status_page_uid = ?", statusPageUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return pub, nil
}

// ListIncidentPublications returns publications matching the filter, newest
// first.
func (s *Service) ListIncidentPublications(
	ctx context.Context, filter *models.ListIncidentPublicationsFilter,
) ([]*models.IncidentPublication, error) {
	var pubs []*models.IncidentPublication

	query := s.db.NewSelect().
		Model(&pubs).
		Where("deleted_at IS NULL").
		Order("published_at DESC")

	if filter.OrganizationUID != "" {
		query = query.Where("organization_uid = ?", filter.OrganizationUID)
	}

	if filter.StatusPageUID != "" {
		query = query.Where("status_page_uid = ?", filter.StatusPageUID)
	}

	if filter.IncidentUID != "" {
		query = query.Where("incident_uid = ?", filter.IncidentUID)
	}

	if filter.State != "" {
		query = query.Where("public_state = ?", filter.State)
	}

	if filter.ActiveOnly {
		query = query.Where("public_state <> ?", string(models.PublicationStateResolved))
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

	return pubs, nil
}

// UpdateIncidentPublication applies a tri-state patch. Nothing but the listed
// columns (plus updated_at) is written, so two concurrent writers touching
// different fields do not clobber each other's work.
func (s *Service) UpdateIncidentPublication(
	ctx context.Context, uid string, update *models.IncidentPublicationUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.IncidentPublication)(nil)).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL")

	if update.PublicTitle != nil {
		query = query.Set("public_title = ?", *update.PublicTitle)
	}

	if update.PublicState != nil {
		query = query.Set("public_state = ?", string(*update.PublicState))
	}

	if update.ClearSeverity {
		query = query.Set("severity = NULL")
	} else if update.Severity != nil {
		query = query.Set("severity = ?", *update.Severity)
	}

	if update.HumanTouchedAt != nil {
		query = query.Set("human_touched_at = ?", *update.HumanTouchedAt)
	}

	if update.ClearResolvedAt {
		query = query.Set("resolved_at = NULL")
	} else if update.ResolvedAt != nil {
		query = query.Set("resolved_at = ?", *update.ResolvedAt)
	}

	if update.NotifyWindowStart != nil {
		query = query.Set("notify_window_start = ?", *update.NotifyWindowStart)
	}

	if update.NotifyWindowCount != nil {
		query = query.Set("notify_window_count = ?", *update.NotifyWindowCount)
	}

	_, err := query.Exec(ctx)

	return err
}

// SoftDeleteIncidentPublication unpublishes: the row stays for audit, but it
// leaves both the public page and the partial unique index, so the same
// incident can be republished later.
func (s *Service) SoftDeleteIncidentPublication(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.IncidentPublication)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Exec(ctx)

	return err
}

// CountIncidentPublicationsForIncident counts live publications referencing an
// incident. The retention guard reads this before deleting an incident row.
func (s *Service) CountIncidentPublicationsForIncident(ctx context.Context, incidentUID string) (int, error) {
	return s.db.NewSelect().
		Model((*models.IncidentPublication)(nil)).
		Where("incident_uid = ?", incidentUID).
		Where("deleted_at IS NULL").
		Count(ctx)
}

// ListStatusPageTargetsForCheck returns every live status-page resource that
// displays the check — directly, or through the check's group.
func (s *Service) ListStatusPageTargetsForCheck(
	ctx context.Context, checkUID string, checkGroupUID *string,
) ([]*db.StatusPageTarget, error) {
	type row struct {
		PageUID     string  `bun:"page_uid"`
		SectionUID  string  `bun:"section_uid"`
		ResourceUID string  `bun:"resource_uid"`
		AutoPublish *bool   `bun:"auto_publish"`
		PublicName  *string `bun:"public_name"`
		GroupUID    *string `bun:"check_group_uid"`
	}

	var rows []row

	query := s.db.NewSelect().
		TableExpr("status_page_resources AS r").
		ColumnExpr("sec.status_page_uid AS page_uid").
		ColumnExpr("r.section_uid AS section_uid").
		ColumnExpr("r.uid AS resource_uid").
		ColumnExpr("r.auto_publish AS auto_publish").
		ColumnExpr("r.public_name AS public_name").
		ColumnExpr("r.check_group_uid AS check_group_uid").
		Join("JOIN status_page_sections AS sec ON sec.uid = r.section_uid").
		Join("JOIN status_pages AS p ON p.uid = sec.status_page_uid").
		Where("sec.deleted_at IS NULL").
		Where("p.deleted_at IS NULL")

	if checkGroupUID != nil && *checkGroupUID != "" {
		query = query.Where("r.check_uid = ? OR r.check_group_uid = ?", checkUID, *checkGroupUID)
	} else {
		query = query.Where("r.check_uid = ?", checkUID)
	}

	if err := query.Scan(ctx, &rows); err != nil {
		return nil, err
	}

	targets := make([]*db.StatusPageTarget, 0, len(rows))
	for idx := range rows {
		entry := &rows[idx]
		targets = append(targets, &db.StatusPageTarget{
			PageUID:             entry.PageUID,
			SectionUID:          entry.SectionUID,
			ResourceUID:         entry.ResourceUID,
			ResourceAutoPublish: entry.AutoPublish,
			ResourcePublicName:  entry.PublicName,
			ViaGroup:            entry.GroupUID != nil && *entry.GroupUID != "",
		})
	}

	return targets, nil
}
