package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestProcessCheckResultWarningIsIncidentNeutral pins the headline behaviour
// of the live Warning status (Decision B): a raw Warning with no open incident
// updates the visible status to Warning but never opens an incident and never
// arms the confirmation clock — it is display-only, modelled on Validating.
func TestProcessCheckResultWarningIsIncidentNeutral(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 0) // confirmation=0 would open immediately for a failure

	s.submit(t, models.ResultStatusWarning)

	c := s.reload(t)
	r.Equal(models.CheckStatusWarning, c.Status,
		"a raw warning must set the visible status to warning")
	r.Nil(c.FirstFailureAt,
		"warning is clock-neutral: first_failure_at must not be armed")
	r.False(s.hasActiveIncident(t),
		"warning must never open an incident, even with confirmation=0")
}

// TestProcessCheckResultWarningDuringOutageDoesNotResolve verifies that a
// Warning arriving while an incident is open neither resolves the incident nor
// clears the recovery path: the incident stays active, and the visible status
// reflects the latest signal (Warning).
func TestProcessCheckResultWarningDuringOutageDoesNotResolve(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 0) // open immediately on first failure

	s.submit(t, models.ResultStatusDown)
	r.Equal(models.CheckStatusDown, s.reload(t).Status)
	r.True(s.hasActiveIncident(t), "first failure opens the incident")

	s.submit(t, models.ResultStatusWarning)
	c := s.reload(t)
	r.Equal(models.CheckStatusWarning, c.Status,
		"warning reflects the latest live signal even during an open incident")
	r.True(s.hasActiveIncident(t),
		"warning must not resolve an open incident")
}

// TestProcessCheckResultWarningThenUpClears confirms a later Up clears the
// neutral warning state and the streak behaves across up<->warning edges.
func TestProcessCheckResultWarningThenUpClears(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 60)

	// Two consecutive warnings: streak grows, status stays warning.
	s.submit(t, models.ResultStatusWarning)
	first := s.reload(t)
	r.Equal(models.CheckStatusWarning, first.Status)
	r.Equal(1, first.StatusStreak, "first warning resets streak to 1")

	s.submit(t, models.ResultStatusWarning)
	second := s.reload(t)
	r.Equal(models.CheckStatusWarning, second.Status)
	r.Equal(2, second.StatusStreak, "consecutive warnings grow the streak")

	// Up clears it: status flips, streak resets, no incident anywhere.
	s.submit(t, models.ResultStatusUp)
	c := s.reload(t)
	r.Equal(models.CheckStatusUp, c.Status, "a later up clears the warning state")
	r.Equal(1, c.StatusStreak, "up<->warning edge resets the streak")
	r.False(s.hasActiveIncident(t),
		"a warning->up sequence must never have opened an incident")
}

// TestProcessCheckResultWarningStatusChangedAtBumpsOnEdge verifies the
// up<->warning edge bumps status_changed_at (warning is not a failure
// sub-state like validating<->down). The check starts Up, so the up->warning
// transition is the first real status change and must set status_changed_at.
func TestProcessCheckResultWarningStatusChangedAtBumpsOnEdge(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newValidatingSetup(t, 60) // check is created with status Up

	before := time.Now()
	s.submit(t, models.ResultStatusWarning)

	c := s.reload(t)
	r.Equal(models.CheckStatusWarning, c.Status)
	r.NotNil(c.StatusChangedAt,
		"the up->warning edge must set status_changed_at")
	r.False(c.StatusChangedAt.Before(before),
		"status_changed_at must be stamped at (or after) the transition time")

	// A subsequent warning does NOT bump it (continuing same state).
	changedAt := *c.StatusChangedAt
	s.submit(t, models.ResultStatusWarning)
	c2 := s.reload(t)
	r.NotNil(c2.StatusChangedAt)
	r.True(c2.StatusChangedAt.Equal(changedAt),
		"a continuing warning must not re-bump status_changed_at")
}
