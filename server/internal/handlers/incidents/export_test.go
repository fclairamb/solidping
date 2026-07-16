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
