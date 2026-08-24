package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateCheckDependency inserts a new edge.
func (s *Service) CreateCheckDependency(ctx context.Context, dep *models.CheckDependency) error {
	_, err := s.db.NewInsert().Model(dep).Exec(ctx)
	if err != nil {
		return fmt.Errorf("create check dependency: %w", err)
	}

	return nil
}

// GetCheckDependency fetches a single edge by UID, scoped to its organization.
func (s *Service) GetCheckDependency(
	ctx context.Context, orgUID, depUID string,
) (*models.CheckDependency, error) {
	dep := new(models.CheckDependency)

	err := s.db.NewSelect().
		Model(dep).
		Where("uid = ?", depUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get check dependency: %w", err)
	}

	return dep, nil
}

// ListCheckDependenciesByOrg returns every active edge in the org.
func (s *Service) ListCheckDependenciesByOrg(
	ctx context.Context, orgUID string,
) ([]*models.CheckDependency, error) {
	var deps []*models.CheckDependency

	err := s.db.NewSelect().
		Model(&deps).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list check dependencies by org: %w", err)
	}

	return deps, nil
}

// ListCheckDependencyParents returns the active edges where this check is the child.
func (s *Service) ListCheckDependencyParents(
	ctx context.Context, childCheckUID string,
) ([]*models.CheckDependency, error) {
	var deps []*models.CheckDependency

	err := s.db.NewSelect().
		Model(&deps).
		Where("child_check_uid = ?", childCheckUID).
		Where("deleted_at IS NULL").
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list dependency parents: %w", err)
	}

	return deps, nil
}

// ListCheckDependencyChildren returns the active edges where this check is the parent.
func (s *Service) ListCheckDependencyChildren(
	ctx context.Context, parentCheckUID string,
) ([]*models.CheckDependency, error) {
	var deps []*models.CheckDependency

	err := s.db.NewSelect().
		Model(&deps).
		Where("parent_check_uid = ?", parentCheckUID).
		Where("deleted_at IS NULL").
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list dependency children: %w", err)
	}

	return deps, nil
}

// FindCheckDependencyEdge returns the edge for the (parent, child) pair if it exists.
func (s *Service) FindCheckDependencyEdge(
	ctx context.Context, parentUID, childUID string,
) (*models.CheckDependency, error) {
	dep := new(models.CheckDependency)

	err := s.db.NewSelect().
		Model(dep).
		Where("parent_check_uid = ?", parentUID).
		Where("child_check_uid = ?", childUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return dep, nil
}

// UpdateCheckDependency writes the supplied fields. Empty update is a no-op.
func (s *Service) UpdateCheckDependency(
	ctx context.Context, depUID string, update *models.CheckDependencyUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.CheckDependency)(nil)).
		Where("uid = ?", depUID).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	hasChange := false

	if update.Kind != nil {
		query = query.Set("kind = ?", *update.Kind)
		hasChange = true
	}

	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
		hasChange = true
	}

	if update.ClearDescription {
		query = query.Set("description = NULL")
		hasChange = true
	}

	if !hasChange {
		return nil
	}

	_, err := query.Exec(ctx)
	if err != nil {
		return fmt.Errorf("update check dependency: %w", err)
	}

	return nil
}

// DeleteCheckDependency soft-deletes the edge.
func (s *Service) DeleteCheckDependency(ctx context.Context, depUID string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.CheckDependency)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", depUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete check dependency: %w", err)
	}

	return nil
}

// DeleteCheckDependenciesForCheck soft-deletes every active edge where
// checkUID is either the parent or the child. Called from check deletion so
// dependency edges don't outlive the check they reference.
func (s *Service) DeleteCheckDependenciesForCheck(ctx context.Context, checkUID string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.CheckDependency)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("(parent_check_uid = ? OR child_check_uid = ?)", checkUID, checkUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete check dependencies for check: %w", err)
	}

	return nil
}

// ListSuppressedChildIncidents returns active incidents rolled up under the parent.
func (s *Service) ListSuppressedChildIncidents(
	ctx context.Context, parentIncidentUID string,
) ([]*models.Incident, error) {
	var incidents []*models.Incident

	err := s.db.NewSelect().
		Model(&incidents).
		Where("caused_by_incident_uid = ?", parentIncidentUID).
		Where("paging_suppressed = ?", true).
		Where("state = ?", models.IncidentStateActive).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list suppressed child incidents: %w", err)
	}

	return incidents, nil
}

// FindActiveIncidentsForChecksInWindow returns the active, non-suppressed incidents
// for the listed checks whose started_at is within [since, until].
func (s *Service) FindActiveIncidentsForChecksInWindow(
	ctx context.Context, checkUIDs []string, since, until time.Time,
) ([]*models.Incident, error) {
	if len(checkUIDs) == 0 {
		return nil, nil
	}

	var incidents []*models.Incident

	err := s.db.NewSelect().
		Model(&incidents).
		// Check incidents only: rolling a real outage up under a burn alert
		// would suppress its paging on the strength of a budget statistic.
		Where("kind = ?", models.IncidentKindCheck).
		Where("check_uid IN (?)", bun.List(checkUIDs)).
		Where("state = ?", models.IncidentStateActive).
		Where("paging_suppressed = ?", false).
		Where("started_at >= ?", since).
		Where("started_at <= ?", until).
		Where("deleted_at IS NULL").
		Order("started_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("find active incidents for checks in window: %w", err)
	}

	return incidents, nil
}

// AttachIncidentToRollupParent attaches a child incident to a hard-parent
// incident, guarded on the child still being active and NOT already
// suppressed — see the postgres twin for why the guard is load-bearing.
// Returns true when this call performed the update.
func (s *Service) AttachIncidentToRollupParent(
	ctx context.Context, childIncidentUID, parentIncidentUID string,
) (bool, error) {
	res, err := s.db.NewUpdate().
		Model((*models.Incident)(nil)).
		Set("caused_by_incident_uid = ?", parentIncidentUID).
		Set("paging_suppressed = ?", true).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", childIncidentUID).
		Where("paging_suppressed = ?", false).
		Where("state = ?", models.IncidentStateActive).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("attach incident to rollup parent: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("attach incident to rollup parent rows affected: %w", err)
	}

	return affected > 0, nil
}
