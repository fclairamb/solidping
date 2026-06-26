package incidents

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ApplyRollupForTest exposes the unexported applyRollup so external
// (incidents_test) tests can exercise the real rollup-attribution path when
// building suppressed child incidents.
func (s *Service) ApplyRollupForTest(ctx context.Context, check *models.Check, incident *models.Incident) {
	s.applyRollup(ctx, check, incident)
}
