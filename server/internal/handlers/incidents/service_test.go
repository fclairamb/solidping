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

// flapCheck builds an in-memory Check carrying the given flap config + state so
// the pure effectiveRecoveryPeriod / recoveryElapsed helpers can be exercised
// without a DB. R is the base recovery period in seconds.
func flapCheck(recoverySeconds, window, factor, capMult, flapCount int) *models.Check {
	return &models.Check{
		RecoveryPeriodSeconds: recoverySeconds,
		FlappingWindowSeconds: window,
		FlapBackoffFactor:     factor,
		MaxRecoveryMultiplier: capMult,
		FlapCount:             flapCount,
	}
}

// TestEffectiveRecoveryPeriodWorkedExample reproduces the spec's worked example
// (R = 2 min, F = 2, window = 6h, CAP = 8): the required recovery period
// doubles per flap and then holds at the cap (R × 8 = 16 min).
func TestEffectiveRecoveryPeriodWorkedExample(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const (
		recovery = 120   // 2 min
		window   = 21600 // 6h
		factor   = 2
		capMult  = 8
	)

	cases := []struct {
		name      string
		flapCount int
		want      time.Duration
	}{
		{"1st outage (k=0)", 0, 2 * time.Minute},
		{"2nd outage (k=1)", 1, 4 * time.Minute},
		{"3rd outage (k=2)", 2, 8 * time.Minute},
		{"4th outage (k=3)", 3, 16 * time.Minute},
		{"5th outage (k=4) holds at cap", 4, 16 * time.Minute}, // R·2^4=32m → cap R·8=16m
		{"6th outage (k=5) still at cap", 5, 16 * time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check := flapCheck(recovery, window, factor, capMult, tc.flapCount)
			r.Equal(tc.want, incidents.EffectiveRecoveryPeriodForTest(check),
				"flapCount=%d", tc.flapCount)
		})
	}
}

// TestEffectiveRecoveryPeriodCapHolds proves the cap multiplier is genuinely
// consumed (not hard-coded to the default 8): with CAP = 4 the required
// recovery doubles to R×4 and then holds there, even as more flaps accumulate.
func TestEffectiveRecoveryPeriodCapHolds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const (
		recovery = 120   // 2 min
		window   = 21600 // 6h
		factor   = 2
		capMult  = 4 // deliberately below the default to exercise the cap
	)

	cases := []struct {
		name      string
		flapCount int
		want      time.Duration
	}{
		{"k=1 below cap", 1, 4 * time.Minute},
		{"k=2 reaches cap", 2, 8 * time.Minute},  // R·2^2 = 8m = R·CAP
		{"k=3 holds at cap", 3, 8 * time.Minute}, // R·2^3 = 16m → cap R·4 = 8m
		{"k=4 still at cap", 4, 8 * time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check := flapCheck(recovery, window, factor, capMult, tc.flapCount)
			r.Equal(tc.want, incidents.EffectiveRecoveryPeriodForTest(check),
				"flapCount=%d", tc.flapCount)
		})
	}
}

// TestEffectiveRecoveryPeriodHardCeiling proves the wall-clock HARD_CEILING
// (30 min) clamps even when R × CAP would exceed it.
func TestEffectiveRecoveryPeriodHardCeiling(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// R = 10 min, CAP = 8 → R×8 = 80 min, but ceiling is 30 min.
	check := flapCheck(600, 21600, 2, 8, 6)
	r.Equal(30*time.Minute, incidents.EffectiveRecoveryPeriodForTest(check),
		"effective recovery must be clamped to the 30m hard ceiling")
}

// TestEffectiveRecoveryPeriodOffSwitches pins the off-by-default-equivalent
// guarantees: F=1, window=0, or no flaps accumulated all reproduce constant R.
func TestEffectiveRecoveryPeriodOffSwitches(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const recovery = 120

	t.Run("F=1 is constant R", func(t *testing.T) {
		t.Parallel()
		// Even with several flaps, factor 1 never escalates.
		check := flapCheck(recovery, 21600, 1, 8, 5)
		r.Equal(2*time.Minute, incidents.EffectiveRecoveryPeriodForTest(check))
	})

	t.Run("window=0 is constant R", func(t *testing.T) {
		t.Parallel()
		check := flapCheck(recovery, 0, 2, 8, 5)
		r.Equal(2*time.Minute, incidents.EffectiveRecoveryPeriodForTest(check))
	})

	t.Run("flapCount=0 is constant R", func(t *testing.T) {
		t.Parallel()
		check := flapCheck(recovery, 21600, 2, 8, 0)
		r.Equal(2*time.Minute, incidents.EffectiveRecoveryPeriodForTest(check))
	})
}

// TestRecoveryElapsedFlapAware drives the flap-aware auto-resolve gate against
// an injected clock baseline: a 4-min stability requirement (R=2m, one flap) is
// not met at 3 min but is met at 4 min.
func TestRecoveryElapsedFlapAware(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	firstSuccess := base
	check := flapCheck(120, 21600, 2, 8, 1) // one flap → required 4 min
	check.FirstSuccessSinceFailureAt = &firstSuccess

	r.False(incidents.RecoveryElapsedForTest(check, nil, base.Add(3*time.Minute)),
		"3 min of stability must not satisfy a 4-min flap requirement")
	r.True(incidents.RecoveryElapsedForTest(check, nil, base.Add(4*time.Minute)),
		"4 min of stability satisfies the flap requirement")
}

// TestBumpFlapWindowResetAndIncrement pins the rolling-window rule: outages
// inside the window increment the counter; an outage after a full calm window
// resets it to 0.
func TestBumpFlapWindowResetAndIncrement(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	check := flapCheck(120, 21600, 2, 8, 0) // 6h window
	check.LastOutageAt = nil

	// First-ever outage: no prior outage → count stays 0, last_outage set.
	incidents.BumpFlapForTest(check, base)
	r.Equal(0, check.FlapCount)
	r.NotNil(check.LastOutageAt)

	// Outage 1h later, inside the 6h window → increment.
	incidents.BumpFlapForTest(check, base.Add(time.Hour))
	r.Equal(1, check.FlapCount)

	// Another 2h later, still inside → increment again.
	incidents.BumpFlapForTest(check, base.Add(3*time.Hour))
	r.Equal(2, check.FlapCount)

	// Now 7h of calm beyond the last outage (> 6h window) → reset to 0.
	incidents.BumpFlapForTest(check, base.Add(3*time.Hour).Add(7*time.Hour))
	r.Equal(0, check.FlapCount, "an outage after a full calm window resets the flap count")
}

// --- DB-backed flap-state + reopen tests ---

// flapSetup spins up an in-memory sqlite world with a FAKE clock so tests can
// advance wall-clock time across reopen cooldowns and assert persisted flap
// state. The check opens incidents immediately (confirmation 0) and resolves
// immediately on success (base recovery 0) to keep the focus on flap counting.
type flapSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	clk   *clock.Fake
	org   *models.Organization
	check *models.Check
}

func newFlapSetup(t *testing.T) *flapSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	clk := clock.NewFake(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization("flap-test", "Flap Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0 // open immediately on first failure
	check.RecoveryPeriodSeconds = 0     // resolve immediately on first success (base)
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &flapSetup{svc: svc, dbSvc: dbSvc, clk: clk, org: org, check: check}
}

func (s *flapSetup) submit(t *testing.T, status models.ResultStatus) {
	t.Helper()
	result := models.NewResult(s.org.UID, s.check.UID, status, 0)
	result.PeriodStart = s.clk.Now()
	require.NoError(t, s.dbSvc.CreateResult(t.Context(), result))
	require.NoError(t, s.svc.ProcessCheckResult(context.Background(), s.check, result))
}

func (s *flapSetup) reload(t *testing.T) *models.Check {
	t.Helper()
	c, err := s.dbSvc.GetCheck(t.Context(), s.org.UID, s.check.UID)
	require.NoError(t, err)
	// Keep the in-memory copy the service mutates in sync with persisted state
	// so the next ProcessCheckResult call reads fresh flap state off the row.
	s.check = c
	return c
}

func (s *flapSetup) activeIncident(t *testing.T) *models.Incident {
	t.Helper()
	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	require.NoError(t, err)
	return inc
}

// TestReopenCountsAsFlapAndAdaptsRecovery is the integration proof that a fast
// relapse (a) reopens the SAME incident bumping RelapseCount and (b) counts as
// a flap, escalating the effective recovery threshold. With reopen_cooldown
// default (clamped to 2 min) and a 6h flapping window, two close
// down→up→down cycles drive flap_count to 1 then 2.
func TestReopenCountsAsFlapAndAdaptsRecovery(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newFlapSetup(t)

	// Cycle 0: first outage opens an incident. flap_count stays 0 (first ever).
	s.submit(t, models.ResultStatusDown)
	inc := s.activeIncident(t)
	r.NotNil(inc, "first failure opens an incident")
	r.Equal(0, inc.RelapseCount)
	c := s.reload(t)
	r.Equal(0, c.FlapCount, "first-ever outage does not count as a flap")
	r.NotNil(c.LastOutageAt, "last_outage_at armed on first outage")

	// Recover (resolves immediately, base recovery 0).
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.Nil(s.activeIncident(t), "success resolves the incident")
	s.reload(t)

	// Relapse inside the reopen cooldown → reopen SAME incident + flap++.
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusDown)
	inc = s.activeIncident(t)
	r.NotNil(inc)
	r.Equal(1, inc.RelapseCount, "fast relapse reopens the same incident (RelapseCount++)")
	c = s.reload(t)
	r.Equal(1, c.FlapCount, "a reopen counts as a flap")

	// Recover + relapse again → flap_count 2.
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	s.reload(t)
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusDown)
	inc = s.activeIncident(t)
	r.Equal(2, inc.RelapseCount, "second relapse keeps reattaching to the same incident")
	c = s.reload(t)
	r.Equal(2, c.FlapCount, "second reopen escalates the flap count")
}

// TestFastRelapseReopensIndependentOfFlapBackoff pins the spec's independence
// guarantee: even with flapping fully OFF (factor 1), a fast relapse within the
// reopen cooldown still reopens the same incident and bumps RelapseCount — the
// short reopen window and the long flapping window are independent layers.
func TestFastRelapseReopensIndependentOfFlapBackoff(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newFlapSetup(t)

	// Turn the flapping backoff off; reopen window stays active.
	off := 1
	r.NoError(s.dbSvc.UpdateCheck(t.Context(), s.check.UID, &models.CheckUpdate{
		FlapBackoffFactor: &off,
	}))
	s.reload(t)

	s.submit(t, models.ResultStatusDown)
	first := s.activeIncident(t)
	r.NotNil(first)
	r.Equal(0, first.RelapseCount)

	s.clk.Advance(20 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.Nil(s.activeIncident(t))
	s.reload(t)

	s.clk.Advance(20 * time.Second)
	s.submit(t, models.ResultStatusDown)
	reopened := s.activeIncident(t)
	r.NotNil(reopened)
	r.Equal(first.UID, reopened.UID, "fast relapse reattaches to the same incident even with flapping off")
	r.Equal(1, reopened.RelapseCount, "RelapseCount increments independent of flap backoff")
}
