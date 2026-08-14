package incidents

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ApplyRollupForTest exposes the unexported applyRollup so external
// (incidents_test) tests can exercise the real rollup-attribution path when
// building suppressed child incidents.
func (s *Service) ApplyRollupForTest(ctx context.Context, check *models.Check, incident *models.Incident) {
	s.applyRollup(ctx, check, incident)
}

// EffectiveRecoveryPeriodForTest exposes the unexported effectiveRecoveryPeriod
// so external tests can assert the flapping-backoff math (worked example, cap,
// off-switches) directly against a check's flap state.
func EffectiveRecoveryPeriodForTest(check *models.Check) time.Duration {
	return effectiveRecoveryPeriod(check)
}

// RecoveryElapsedForTest exposes the unexported recoveryElapsed so external
// tests can assert the flap-aware auto-resolve gate against the injected clock.
// A nil incident skips the incident-scoping guard, exercising the flap math
// alone.
func RecoveryElapsedForTest(check *models.Check, incident *models.Incident, now time.Time) bool {
	return recoveryElapsed(check, incident, now)
}

// IncidentClockFloorForTest exposes the unexported incidentClockFloor so
// external tests can assert which onset the recovery clock is scoped to.
func IncidentClockFloorForTest(incident *models.Incident) time.Time {
	return incidentClockFloor(incident)
}

// BumpFlapForTest exposes the unexported bumpFlap so external tests can verify
// the rolling window reset/increment rule without a full DB round-trip.
func BumpFlapForTest(check *models.Check, now time.Time) {
	bumpFlap(check, now)
}

// CreateIncidentForTest exposes the unexported createIncident so external
// tests can assert the failure-details snapshot without driving the full
// confirmation-threshold state machine in ProcessCheckResult.
func (s *Service) CreateIncidentForTest(ctx context.Context, check *models.Check, result *models.Result) error {
	return s.createIncident(ctx, check, result)
}

// CreateOrReopenIncidentForTest exposes the unexported createOrReopenIncident
// so external tests can drive the per-check open/reopen decision directly.
func (s *Service) CreateOrReopenIncidentForTest(
	ctx context.Context, check *models.Check, result *models.Result,
) error {
	return s.createOrReopenIncident(ctx, check, result)
}

// CreateGroupIncidentForTest exposes the unexported createGroupIncident so
// external tests can assert the group-create path writes the same
// failure-details snapshot as the per-check path.
func (s *Service) CreateGroupIncidentForTest(ctx context.Context, check *models.Check, result *models.Result) error {
	return s.createGroupIncident(ctx, check, result)
}

// CreateOrReopenGroupIncidentForTest exposes the unexported
// createOrReopenGroupIncident so external tests can drive the group
// open/reopen decision directly.
func (s *Service) CreateOrReopenGroupIncidentForTest(
	ctx context.Context, check *models.Check, result *models.Result,
) error {
	return s.createOrReopenGroupIncident(ctx, check, result)
}

// FailureDetailsForTest exposes the unexported failureDetails helper so
// external tests can assert the snapshot shape (and the size cap) in
// isolation, without a DB round-trip.
func FailureDetailsForTest(result *models.Result) models.JSONMap {
	return failureDetails(result)
}
