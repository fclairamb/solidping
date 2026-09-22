package postgres

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ListChecksForDegradedEval returns the degraded evaluator's work queue (spec
// 2026-09-22-03).
//
// Enabled, non-internal, live checks only: a paused check has no probe stream to
// reason about, and the internal self-stats checks are plumbing nobody wants an
// intermittence notification for.
//
// Every check is returned regardless of `degraded_enabled` — that flag gates
// OPENING an incident, not evaluating. The dry run is the whole adoption path,
// so a disabled check still has to be swept in order to be stamped.
//
// Ordered oldest-evaluated first (NULLs, i.e. never evaluated, first) so a
// bounded per-sweep limit still gives every check a turn on a large install
// instead of starving the tail forever — the same rotation
// ListEnabledSLOAlertPolicies uses.
func (s *Service) ListChecksForDegradedEval(ctx context.Context, limit int) ([]*models.Check, error) {
	var checks []*models.Check

	query := s.db.NewSelect().
		Model(&checks).
		Where("enabled = ?", true).
		Where("internal = ?", false).
		Where("deleted_at IS NULL").
		Order("degraded_evaluated_at ASC NULLS FIRST")

	if limit > 0 {
		query = query.Limit(limit)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("list checks for degraded eval: %w", err)
	}

	return checks, nil
}

// FindActiveDegradedIncident returns the open degraded incident for a check, or
// sql.ErrNoRows when there is none.
//
// Scoped to kind='degraded' for the same reason FindActiveIncidentByCheckUID is
// scoped to kind='check': the two state machines share a check_uid and must not
// read each other's rows as their own.
func (s *Service) FindActiveDegradedIncident(ctx context.Context, checkUID string) (*models.Incident, error) {
	incident := new(models.Incident)

	err := s.db.NewSelect().
		Model(incident).
		Where("kind = ?", models.IncidentKindDegraded).
		Where("check_uid = ?", checkUID).
		Where("state = ?", models.IncidentStateActive).
		Where("deleted_at IS NULL").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return incident, nil
}
