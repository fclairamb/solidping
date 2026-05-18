package incidents_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// validatingSetup spins up the smallest world that can drive
// ProcessCheckResult: an in-memory db, an org, and a check with a
// configurable confirmation period. Mirrors resolveSetup but skips the
// pre-existing incident and connection — these tests are about the
// state machine, not notifications.
type validatingSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	org   *models.Organization
	check *models.Check
}

// newValidatingSetup creates a check with the given confirmation period
// in seconds. 0 = "open immediately on first failure". Use
// backdateFirstFailure to simulate a check that has been failing for
// long enough to cross the period.
func newValidatingSetup(t *testing.T, confirmationSeconds int) *validatingSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB())
	svc := incidents.NewService(dbSvc, jobs, clock.Real{})

	org := models.NewOrganization("validating-test", "Validating Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = confirmationSeconds
	check.RecoveryPeriodSeconds = 0
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &validatingSetup{svc: svc, dbSvc: dbSvc, org: org, check: check}
}

// reload fetches the check fresh from the DB so the test asserts on
// persisted state, not the in-memory copy mutated by ProcessCheckResult.
func (s *validatingSetup) reload(t *testing.T) *models.Check {
	t.Helper()
	c, err := s.dbSvc.GetCheck(t.Context(), s.org.UID, s.check.UID)
	require.NoError(t, err)
	return c
}

func (s *validatingSetup) hasActiveIncident(t *testing.T) bool {
	t.Helper()
	_, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	require.NoError(t, err)
	return true
}

func (s *validatingSetup) submit(t *testing.T, status models.ResultStatus) {
	t.Helper()
	result := models.NewResult(s.org.UID, s.check.UID, status, 0)
	require.NoError(t, s.dbSvc.CreateResult(t.Context(), result))
	require.NoError(t, s.svc.ProcessCheckResult(context.Background(), s.check, result))
}

// backdateFirstFailure rewrites first_failure_at to a past time so the
// next failure result sees the confirmation period as elapsed without
// the test having to actually wait. Mirrors how production rows behave
// after the configured period passes; we'd otherwise need a fake clock.
func (s *validatingSetup) backdateFirstFailure(t *testing.T, ago time.Duration) {
	t.Helper()
	past := time.Now().Add(-ago)
	require.NoError(t, s.dbSvc.UpdateCheckIncidentClocks(
		t.Context(), s.check.UID, &past, false, nil, false,
	))
	s.check.FirstFailureAt = &past
}

// TestProcessCheckResultEntersValidatingOnFirstFailure pins the headline
// behavior of the new state: the very first failure flips the check to
// validating (not down) when the confirmation period hasn't elapsed.
func TestProcessCheckResultEntersValidatingOnFirstFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 60) // 60s confirmation

	s.submit(t, models.ResultStatusDown)

	c := s.reload(t)
	r.Equal(models.CheckStatusValidating, c.Status,
		"first failure must flip to validating, not down")
	r.NotNil(c.FirstFailureAt, "first_failure_at must be armed")
	r.False(s.hasActiveIncident(t),
		"no incident should open while inside the confirmation window")
}

// TestProcessCheckResultStaysValidatingUntilPeriodElapses confirms that
// failures within the configured window stay validating, and the
// failure that crosses the wall-clock threshold opens the incident.
func TestProcessCheckResultStaysValidatingUntilPeriodElapses(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 60)

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusValidating, s.reload(t).Status)

	s.submit(t, models.ResultStatusDown)
	c := s.reload(t)
	r.Equal(models.CheckStatusValidating, c.Status,
		"failure within window stays validating")
	r.False(s.hasActiveIncident(t))

	// Simulate "we've been failing for 65 seconds now" — the next
	// failure result should cross the 60s threshold.
	s.backdateFirstFailure(t, 65*time.Second)

	s.submit(t, models.ResultStatusDown)
	c = s.reload(t)
	r.Equal(models.CheckStatusDown, c.Status,
		"failure after confirmation window flips to down")
	r.True(s.hasActiveIncident(t),
		"crossing the confirmation period opens an incident")
}

// TestProcessCheckResultRecoversFromValidatingWithoutIncident verifies the
// "transient blip" path: a failure followed by a success must return to
// up without ever opening an incident, and must clear first_failure_at.
func TestProcessCheckResultRecoversFromValidatingWithoutIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 60)

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusValidating, s.reload(t).Status)

	s.submit(t, models.ResultStatusUp)
	c := s.reload(t)
	r.Equal(models.CheckStatusUp, c.Status,
		"success while validating returns to up")
	r.Nil(c.FirstFailureAt,
		"first_failure_at must be cleared on success")
	r.False(s.hasActiveIncident(t),
		"transient validating blip must not open an incident")
}

// TestProcessCheckResultZeroConfirmationOpensImmediately verifies that
// ConfirmationPeriodSeconds=0 (the default) opens the incident on the
// first failure result, matching pre-spec behavior for threshold=1.
func TestProcessCheckResultZeroConfirmationOpensImmediately(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 0)

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusDown, s.reload(t).Status,
		"confirmation=0 means first failure opens immediately")
	r.True(s.hasActiveIncident(t))

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusDown, s.reload(t).Status,
		"subsequent failures with an active incident stay down, never validating")
}

// TestRecoveryFlapResetsClock pins the flap-reset rule from the spec:
// a failure during the recovery window clears first_success_since_failure_at,
// so the recovery clock restarts on the next observed success.
func TestRecoveryFlapResetsClock(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 0)
	s.check.RecoveryPeriodSeconds = 60
	r.NoError(s.dbSvc.UpdateCheck(t.Context(), s.check.UID, &models.CheckUpdate{
		RecoveryPeriodSeconds: &s.check.RecoveryPeriodSeconds,
	}))

	// Open the incident.
	s.submit(t, models.ResultStatusDown)
	r.True(s.hasActiveIncident(t))

	// First success arms the recovery clock.
	s.submit(t, models.ResultStatusUp)
	c := s.reload(t)
	r.NotNil(c.FirstSuccessSinceFailureAt,
		"first success during incident arms the recovery clock")
	r.True(s.hasActiveIncident(t),
		"recovery period 60s; one success doesn't auto-resolve yet")

	// A failure during recovery wipes the clock.
	s.submit(t, models.ResultStatusDown)
	c = s.reload(t)
	r.Nil(c.FirstSuccessSinceFailureAt,
		"failure during recovery window clears first_success_since_failure_at")
	r.True(s.hasActiveIncident(t),
		"incident remains open after flap")
}
