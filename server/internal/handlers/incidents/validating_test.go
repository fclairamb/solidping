package incidents_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// validatingSetup spins up the smallest world that can drive
// ProcessCheckResult: an in-memory db, an org, and a check with a
// configurable IncidentThreshold. Mirrors resolveSetup but skips the
// pre-existing incident and connection — these tests are about the
// streak/state machine, not notifications.
type validatingSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	org   *models.Organization
	check *models.Check
}

func newValidatingSetup(t *testing.T, threshold int) *validatingSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB())
	svc := incidents.NewService(dbSvc, jobs)

	org := models.NewOrganization("validating-test", "Validating Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	check.IncidentThreshold = threshold
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

// TestProcessCheckResultEntersValidatingOnFirstFailure pins the headline
// behavior of the new state: the very first failure flips the check to
// validating, not down, and no incident opens until the streak crosses
// IncidentThreshold.
func TestProcessCheckResultEntersValidatingOnFirstFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 3)

	s.submit(t, models.ResultStatusDown)

	c := s.reload(t)
	r.Equal(models.CheckStatusValidating, c.Status,
		"first failure must flip to validating, not down")
	r.Equal(1, c.StatusStreak)
	r.False(s.hasActiveIncident(t),
		"no incident should open while still in validating")
}

// TestProcessCheckResultStaysValidatingUntilThreshold confirms the
// validating sub-state spans the entire confirmation window — only the
// streak that crosses IncidentThreshold flips to down.
func TestProcessCheckResultStaysValidatingUntilThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 3)

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusValidating, s.reload(t).Status)

	s.submit(t, models.ResultStatusDown)
	c := s.reload(t)
	r.Equal(models.CheckStatusValidating, c.Status,
		"second failure under threshold stays validating")
	r.Equal(2, c.StatusStreak)
	r.False(s.hasActiveIncident(t))

	s.submit(t, models.ResultStatusDown)
	c = s.reload(t)
	r.Equal(models.CheckStatusDown, c.Status,
		"threshold-crossing failure flips to down")
	r.Equal(3, c.StatusStreak)
	r.True(s.hasActiveIncident(t),
		"crossing IncidentThreshold opens an incident")
}

// TestProcessCheckResultRecoversFromValidatingWithoutIncident verifies the
// "transient blip" path: a single failure that's followed by a success
// must return to up without ever opening an incident or persisting any
// down/incident audit trail.
func TestProcessCheckResultRecoversFromValidatingWithoutIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 3)

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusValidating, s.reload(t).Status)

	s.submit(t, models.ResultStatusUp)
	c := s.reload(t)
	r.Equal(models.CheckStatusUp, c.Status,
		"success while validating returns to up")
	r.Equal(1, c.StatusStreak)
	r.False(s.hasActiveIncident(t),
		"transient validating blip must not open an incident")
}

// TestProcessCheckResultDownStaysDown locks in that an already-open
// incident keeps the check in down — validating is purely the
// pre-incident sub-state, never reached on the way back up.
func TestProcessCheckResultDownStaysDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 1)

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusDown, s.reload(t).Status,
		"threshold=1 means the first failure opens an incident immediately")
	r.True(s.hasActiveIncident(t))

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusDown, s.reload(t).Status,
		"subsequent failures with an active incident stay down, never validating")
}
