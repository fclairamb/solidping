package sqlite

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ListChecksForDegradedEval is the SQLite twin of the Postgres implementation —
// see there for the rationale. The only dialect difference is the NULL ordering:
// SQLite sorts NULLs first on an ASC order by default, so `NULLS FIRST` is
// neither needed nor accepted.
func (s *Service) ListChecksForDegradedEval(ctx context.Context, limit int) ([]*models.Check, error) {
	var checks []*models.Check

	query := s.db.NewSelect().
		Model(&checks).
		Where("enabled = ?", true).
		Where("degraded_enabled = ?", true).
		Where("internal = ?", false).
		Where("deleted_at IS NULL").
		Order("degraded_evaluated_at ASC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("list checks for degraded eval: %w", err)
	}

	return checks, nil
}

// FindActiveDegradedIncident returns the open degraded incident for a check, or
// sql.ErrNoRows when there is none. See the Postgres twin.
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
