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
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// recoverySetup drives ProcessCheckResult against an in-memory sqlite and an
// *injected fake clock*, so a multi-incident sequence spanning minutes runs
// instantly and deterministically. Unlike validatingSetup (real clock +
// backdating), advancing time here is explicit — these tests must never depend
// on wall-clock timing.
type recoverySetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	clk   *clock.Fake
	org   *models.Organization
	check *models.Check
	group *models.CheckGroup
}

// recoveryPeriodSeconds mirrors the recovery period of the check that surfaced
// the reported bug (300s ≈ 10 polls at a 30s interval).
const recoveryPeriodSeconds = 300

// newRecoverySetup builds a check with confirmation=0 (open on first failure)
// and recovery=300s. Adaptive flapping is switched off (FlapBackoffFactor=1)
// so effectiveRecoveryPeriod is a constant 300s across incidents: these tests
// are about the recovery *clock*, not the backoff math, which
// TestRecoveryElapsedFlapAware already covers.
//
// grouped=true also creates a check group and attaches the check to it. Since
// spec 2026-08-24-14 that must change nothing about the incident lifecycle —
// which is exactly what the grouped variants of these tests pin.
func newRecoverySetup(t *testing.T, grouped bool) *recoverySetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	// Anchor the fake clock at real "now": incident.StartedAt is derived from
	// result.PeriodStart, which we also drive off this clock, so both sides of
	// every comparison share one timeline.
	clk := clock.NewFake(time.Now().UTC().Truncate(time.Second))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization("recovery-test", "Recovery Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = recoveryPeriodSeconds
	check.FlapBackoffFactor = 1

	s := &recoverySetup{svc: svc, dbSvc: dbSvc, clk: clk, org: org, check: check}

	if grouped {
		group := models.NewCheckGroup(org.UID, "API", "api-group")
		r.NoError(dbSvc.CreateCheckGroup(ctx, group))
		check.CheckGroupUID = &group.UID
		s.group = group
	}

	r.NoError(dbSvc.CreateCheck(ctx, check))

	return s
}

// submit persists a result stamped at the fake clock's current time and runs it
// through the incident state machine.
func (s *recoverySetup) submit(t *testing.T, status models.ResultStatus) {
	t.Helper()
	now := s.clk.Now()
	result := models.NewResult(s.org.UID, s.check.UID, status, 0)
	result.PeriodStart = now
	result.CreatedAt = now
	require.NoError(t, s.dbSvc.CreateResult(t.Context(), result))
	require.NoError(t, s.svc.ProcessCheckResult(context.Background(), s.check, result))
}

// reload fetches the persisted check so assertions read the written row, not
// the in-memory copy ProcessCheckResult mutates.
func (s *recoverySetup) reload(t *testing.T) *models.Check {
	t.Helper()
	c, err := s.dbSvc.GetCheck(t.Context(), s.org.UID, s.check.UID)
	require.NoError(t, err)

	return c
}

func (s *recoverySetup) activeIncident(t *testing.T) *models.Incident {
	t.Helper()
	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	require.NoError(t, err)

	return inc
}

func (s *recoverySetup) hasActiveIncident(t *testing.T) bool {
	t.Helper()

	return s.activeIncident(t) != nil
}

// seedRecoveryClock writes first_success_since_failure_at directly, simulating
// a row left behind by a pre-fix binary (or any writer outside the state
// machine).
func (s *recoverySetup) seedRecoveryClock(t *testing.T, at time.Time) {
	t.Helper()
	c := s.reload(t)
	require.NoError(t, s.dbSvc.UpdateCheckStatusAndClocks(
		t.Context(), s.check.UID, c.Status, c.StatusStreak, nil,
		models.IncidentClockUpdate{FirstSuccessSinceFailureAt: &at},
	))
	s.check.FirstSuccessSinceFailureAt = &at
}

// TestSecondIncidentDoesNotResolveOnFirstSuccess is the reported regression:
// incident #1 opens, recovers and resolves; later the check fails again and a
// second incident opens. That second incident must honor the full recovery
// period — before the fix, the recovery clock left behind by incident #1 made
// it auto-resolve on its very first success, seconds after opening.
func TestSecondIncidentDoesNotResolveOnFirstSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newRecoverySetup(t, false)

	// --- Incident #1: open, recover, resolve.
	s.submit(t, models.ResultStatusDown)
	r.True(s.hasActiveIncident(t), "confirmation=0 opens on the first failure")

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.True(s.hasActiveIncident(t), "first success arms the clock; 300s not elapsed")

	s.clk.Advance(recoveryPeriodSeconds * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t), "incident #1 resolves once the recovery period elapses")

	// --- The stale value: resolving does not clear the clock.
	r.NotNil(s.reload(t).FirstSuccessSinceFailureAt,
		"resolve leaves the clock set — this is the stale value the fix must neutralize")

	// --- Incident #2: opens well past the reopen cooldown, so it is a fresh one.
	s.clk.Advance(30 * time.Minute)
	s.submit(t, models.ResultStatusDown)

	inc2 := s.activeIncident(t)
	r.NotNil(inc2, "a new incident opens on the later failure")
	r.Nil(s.reload(t).FirstSuccessSinceFailureAt,
		"opening an incident must clear the recovery clock left by the previous one")

	// --- The bug: this first success used to resolve incident #2 instantly.
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	c := s.reload(t)
	r.True(s.hasActiveIncident(t),
		"first success must NOT resolve incident #2 — the 300s recovery period applies")
	r.NotNil(c.FirstSuccessSinceFailureAt)
	r.WithinDuration(s.clk.Now(), *c.FirstSuccessSinceFailureAt, time.Second,
		"the clock is armed at this success, not inherited from incident #1")

	// --- Only elapsing the real recovery period, counted from *this* incident's
	// first success, resolves it.
	s.clk.Advance((recoveryPeriodSeconds - 1) * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.True(s.hasActiveIncident(t), "one second short of the period must not resolve")

	s.clk.Advance(time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t), "incident #2 resolves at firstSuccess + recovery")
}

// TestReopenedIncidentDoesNotResolveOnFirstSuccess covers the same regression
// on the reopen path: a relapse inside the cooldown reattaches to the resolved
// incident, and that reopened incident must also honor the recovery period
// rather than inherit the clock from before it resolved.
func TestReopenedIncidentDoesNotResolveOnFirstSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newRecoverySetup(t, false)

	s.submit(t, models.ResultStatusDown)
	first := s.activeIncident(t)
	r.NotNil(first)

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)

	s.clk.Advance(recoveryPeriodSeconds * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t), "incident resolves after the recovery period")

	// Relapse inside the reopen cooldown (5 × 1min period) → reopen, not create.
	s.clk.Advance(time.Minute)
	s.submit(t, models.ResultStatusDown)

	reopened := s.activeIncident(t)
	r.NotNil(reopened)
	r.Equal(first.UID, reopened.UID, "a fast relapse reopens the same incident")
	r.NotNil(reopened.LastReopenedAt)
	r.Nil(s.reload(t).FirstSuccessSinceFailureAt,
		"reopening an incident must clear the pre-resolve recovery clock")

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.True(s.hasActiveIncident(t),
		"first success after a reopen must not resolve the incident")

	s.clk.Advance(recoveryPeriodSeconds * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t), "reopened incident resolves at firstSuccess + recovery")
}

// TestStaleRecoveryClockIsReArmedOnSuccess covers the write-side guard: a row
// that already carries a clock older than the open incident (e.g. written by a
// pre-fix binary, so the clear-on-open never ran for it) must re-arm on its
// next success rather than honor the stale value. This is what lets existing
// rows self-heal without a backfill migration.
func TestStaleRecoveryClockIsReArmedOnSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newRecoverySetup(t, false)

	s.submit(t, models.ResultStatusDown)
	inc := s.activeIncident(t)
	r.NotNil(inc)

	// A clock from an hour ago: 300s of "stability" long since elapsed.
	stale := inc.StartedAt.Add(-time.Hour)
	s.seedRecoveryClock(t, stale)

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)

	c := s.reload(t)
	r.True(s.hasActiveIncident(t),
		"a clock predating the incident must not resolve it on the first success")
	r.NotNil(c.FirstSuccessSinceFailureAt)
	r.WithinDuration(s.clk.Now(), *c.FirstSuccessSinceFailureAt, time.Second,
		"the stale clock is re-armed to now, so the row self-heals")
}

// TestRecoveryElapsedIgnoresClockOlderThanIncident pins the read-side guard
// directly: recoveryElapsed is scoped to the incident it would resolve, and a
// clock predating that incident's onset never satisfies it — regardless of how
// much time has passed.
func TestRecoveryElapsedIgnoresClockOlderThanIncident(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	newCheck := func(firstSuccess time.Time) *models.Check {
		return &models.Check{
			RecoveryPeriodSeconds:      recoveryPeriodSeconds,
			FlapBackoffFactor:          1,
			FirstSuccessSinceFailureAt: &firstSuccess,
		}
	}
	reopened := base.Add(10 * time.Minute)

	tests := []struct {
		name     string
		incident *models.Incident
		check    *models.Check
		now      time.Time
		want     bool
	}{
		{
			name:     "clock predating StartedAt is ignored",
			incident: &models.Incident{StartedAt: base},
			check:    newCheck(base.Add(-time.Hour)),
			now:      base.Add(time.Hour),
			want:     false,
		},
		{
			name:     "clock after StartedAt resolves once the period elapses",
			incident: &models.Incident{StartedAt: base},
			check:    newCheck(base.Add(time.Minute)),
			now:      base.Add(time.Minute + recoveryPeriodSeconds*time.Second),
			want:     true,
		},
		{
			name:     "clock after StartedAt but inside the period does not resolve",
			incident: &models.Incident{StartedAt: base},
			check:    newCheck(base.Add(time.Minute)),
			now:      base.Add(2 * time.Minute),
			want:     false,
		},
		{
			name:     "clock exactly at StartedAt is honored",
			incident: &models.Incident{StartedAt: base},
			check:    newCheck(base),
			now:      base.Add(recoveryPeriodSeconds * time.Second),
			want:     true,
		},
		{
			name:     "reopen moves the floor: a pre-reopen clock is ignored",
			incident: &models.Incident{StartedAt: base, LastReopenedAt: &reopened},
			check:    newCheck(base.Add(time.Minute)),
			now:      reopened.Add(time.Hour),
			want:     false,
		},
		{
			name:     "clock after the reopen is honored",
			incident: &models.Incident{StartedAt: base, LastReopenedAt: &reopened},
			check:    newCheck(reopened.Add(time.Minute)),
			now:      reopened.Add(time.Minute + recoveryPeriodSeconds*time.Second),
			want:     true,
		},
		{
			name:     "nil incident keeps the unscoped legacy behavior",
			incident: nil,
			check:    newCheck(base),
			now:      base.Add(recoveryPeriodSeconds * time.Second),
			want:     true,
		},
		{
			name:     "unarmed clock never resolves",
			incident: &models.Incident{StartedAt: base},
			check:    &models.Check{RecoveryPeriodSeconds: recoveryPeriodSeconds, FlapBackoffFactor: 1},
			now:      base.Add(time.Hour),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want,
				incidents.RecoveryElapsedForTest(tt.check, tt.incident, tt.now))
		})
	}
}

// TestIncidentClockFloorPicksLatestOnset pins which timestamp scopes the
// recovery clock: the incident's most recent onset.
func TestIncidentClockFloorPicksLatestOnset(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	base := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	later := base.Add(time.Hour)
	earlier := base.Add(-time.Hour)

	r.True(incidents.IncidentClockFloorForTest(nil).IsZero(),
		"a nil incident imposes no floor")
	r.Equal(base, incidents.IncidentClockFloorForTest(&models.Incident{StartedAt: base}),
		"never reopened → StartedAt is the onset")
	r.Equal(later, incidents.IncidentClockFloorForTest(
		&models.Incident{StartedAt: base, LastReopenedAt: &later}),
		"reopened → LastReopenedAt is the onset")
	r.Equal(base, incidents.IncidentClockFloorForTest(
		&models.Incident{StartedAt: base, LastReopenedAt: &earlier}),
		"a LastReopenedAt before StartedAt cannot lower the floor")
}

// TestSingleIncidentRecoveryDoesNotRegress guards the happy path the fix could
// plausibly break: one incident, one success, resolve exactly at
// firstSuccess + recovery — not before.
func TestSingleIncidentRecoveryDoesNotRegress(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newRecoverySetup(t, false)

	s.submit(t, models.ResultStatusDown)
	r.True(s.hasActiveIncident(t))

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	c := s.reload(t)
	r.NotNil(c.FirstSuccessSinceFailureAt, "first success arms the recovery clock")
	armed := *c.FirstSuccessSinceFailureAt
	r.True(s.hasActiveIncident(t))

	s.clk.Advance((recoveryPeriodSeconds - 1) * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.True(s.hasActiveIncident(t), "one second short of the recovery period must not resolve")
	r.WithinDuration(armed, *s.reload(t).FirstSuccessSinceFailureAt, time.Second,
		"further successes must not re-arm a valid clock")

	s.clk.Advance(time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t), "resolves exactly at firstSuccess + recovery")
}

// TestZeroRecoveryPeriodStillResolvesImmediately pins the intentional
// resolve-on-first-success semantics of recovery_period_seconds = 0: the
// incident-scoping guard must not get in its way.
func TestZeroRecoveryPeriodStillResolvesImmediately(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newRecoverySetup(t, false)
	s.check.RecoveryPeriodSeconds = 0
	r.NoError(s.dbSvc.UpdateCheck(t.Context(), s.check.UID, &models.CheckUpdate{
		RecoveryPeriodSeconds: &s.check.RecoveryPeriodSeconds,
	}))

	s.submit(t, models.ResultStatusDown)
	r.True(s.hasActiveIncident(t))

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t),
		"recovery=0 means resolve on the first success, by design")
}

// TestGroupedCheckIncidentDoesNotResolveOnFirstSuccess replays the regression
// for a check that belongs to a check group. Since spec 2026-08-24-14 a
// grouped check takes the ordinary per-check state machine, so this also pins
// that group membership no longer changes the incident's identity.
func TestGroupedCheckIncidentDoesNotResolveOnFirstSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newRecoverySetup(t, true)

	// --- Group incident #1: open, recover, resolve.
	s.submit(t, models.ResultStatusDown)
	inc1 := s.activeIncident(t)
	r.NotNil(inc1, "the failure opens an incident")
	r.Nil(inc1.CheckGroupUID, "a grouped check opens a PER-CHECK incident")
	r.Equal(s.check.UID, inc1.CheckUID)

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.True(s.hasActiveIncident(t), "300s recovery: the first success must not resolve")

	s.clk.Advance(recoveryPeriodSeconds * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t), "the incident resolves once the check recovers")
	r.NotNil(s.reload(t).FirstSuccessSinceFailureAt, "the stale clock survives the resolve")

	// --- Incident #2, past the reopen cooldown.
	s.clk.Advance(30 * time.Minute)
	s.submit(t, models.ResultStatusDown)
	inc2 := s.activeIncident(t)
	r.NotNil(inc2)
	r.NotEqual(inc1.UID, inc2.UID, "past the cooldown this is a fresh incident")
	r.Nil(s.reload(t).FirstSuccessSinceFailureAt,
		"opening an incident for a grouped check must clear the previous recovery clock too")

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.True(s.hasActiveIncident(t),
		"first success must NOT resolve the second incident")

	s.clk.Advance(recoveryPeriodSeconds * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.False(s.hasActiveIncident(t),
		"the second incident resolves at firstSuccess + recovery")
}
