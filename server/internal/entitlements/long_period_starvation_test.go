package entitlements_test

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// starvationJob is one region-job of the simulated org: its period and its
// fixed phase (the UID-hash offset within the period), in seconds.
type starvationJob struct {
	name   string
	period time.Duration
	phase  time.Duration
	long   bool
}

// starvationOrg reproduces the prod org of 2026-10-18: 9 one-minute checks,
// 7 five-minute checks and 8 hourly checks, all in one region, against the
// SaaS cap of 10/min (demand ~10.5/min). The phases are the ones read from
// check_jobs. Two hourly checks land one second after a burst of one-minute
// siblings, so the bucket is empty at their only attempt of the hour.
func starvationOrg() []starvationJob {
	sec := func(m, s int) time.Duration { return time.Duration(m)*time.Minute + time.Duration(s)*time.Second }

	jobs := make([]starvationJob, 0, 24)
	for i, s := range []int{0, 1, 14, 15, 26, 29, 39, 42, 45} {
		jobs = append(jobs, starvationJob{name: "minute-" + string(rune('a'+i)), period: time.Minute, phase: sec(0, s)})
	}

	for i, p := range []time.Duration{sec(0, 54), sec(1, 11), sec(3, 39), sec(3, 34), sec(0, 8), sec(0, 40), sec(0, 48)} {
		jobs = append(jobs, starvationJob{name: "five-" + string(rune('a'+i)), period: 5 * time.Minute, phase: p})
	}

	for i, p := range []time.Duration{
		sec(35, 46), sec(6, 1), sec(44, 7), sec(2, 15), sec(0, 39), sec(26, 59), sec(18, 38), sec(40, 35),
	} {
		jobs = append(jobs, starvationJob{name: "hour-" + string(rune('a'+i)), period: time.Hour, phase: p, long: true})
	}

	return jobs
}

// simulateStarvation drives the real per-org token bucket second by second for
// `hours`, with the worker gate's bookkeeping: a run re-anchors
// effective_scheduled_at, a deferral advances scheduled_at by one period and
// keeps it (checkjobsvc.DeferLeaseRateLimited). Same-second arrivals are taken
// in claim order (effective_scheduled_at ascending). With borrow false every job
// reserves normally, which is the behavior before the fix; with borrow true it
// goes through ReserveJobExecution, the production gate. Returns executions per job.
func simulateStarvation(t *testing.T, hours int, borrow bool) (map[string]int, int) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	start := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	now := start
	svc := entitlements.NewService(
		dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0,
		entitlements.WithClock(func() time.Time { return now }),
	)

	specs := starvationOrg()
	jobs := make([]*models.CheckJob, len(specs))

	for i, spec := range specs {
		at := start.Add(spec.phase)
		jobs[i] = &models.CheckJob{
			OrganizationUID:      org.UID,
			Period:               timeutils.Duration(spec.period),
			ScheduledAt:          &at,
			EffectiveScheduledAt: &at,
		}
	}

	executions := make(map[string]int, len(specs))
	total := 0
	end := start.Add(time.Duration(hours) * time.Hour)

	for ; now.Before(end); now = now.Add(time.Second) {
		var due []int

		for i, job := range jobs {
			if job.ScheduledAt.Equal(now) {
				due = append(due, i)
			}
		}

		sort.SliceStable(due, func(a, b int) bool {
			return jobs[due[a]].EffectiveScheduledAt.Before(*jobs[due[b]].EffectiveScheduledAt)
		})

		for _, i := range due {
			job := jobs[i]

			reserve := func() error { return svc.ReserveCheckExecution(ctx, org.UID) }
			if borrow {
				// The production gate.
				reserve = func() error { return svc.ReserveJobExecution(ctx, job) }
			}

			next := job.ScheduledAt.Add(time.Duration(job.Period))
			if reserve() == nil {
				executions[specs[i].name]++
				total++
				job.EffectiveScheduledAt = &next
			}

			job.ScheduledAt = &next
		}
	}

	return executions, total
}

// TestDeferredJobBorrowsSoLongPeriodChecksRun is the regression test for the
// prod watchdog report of 2026-10-19: two hourly checks of an org slightly
// over its cap had no result for 27 hours, while its one-minute checks shared
// the deficit fairly. The control runs the same simulation without borrowing
// and must reproduce the starvation, or the simulation proves nothing.
//
//nolint:paralleltest // two long simulations, each on its own in-memory DB
func TestDeferredJobBorrowsSoLongPeriodChecksRun(t *testing.T) {
	const hours = 12

	t.Run("ControlWithoutBorrowingStarvesAnHourlyCheck", func(t *testing.T) { //nolint:paralleltest // see above
		executions, _ := simulateStarvation(t, hours, false)
		t.Logf("executions without borrowing: %v", executions)

		starved := 0

		for _, spec := range starvationOrg() {
			if spec.long && executions[spec.name] == 0 {
				starved++
			}
		}

		require.Positive(t, starved, "the control must reproduce a starved hourly check: %v", executions)
	})

	t.Run("BorrowingRunsEveryCheckAndKeepsTheCap", func(t *testing.T) { //nolint:paralleltest // see above
		executions, total := simulateStarvation(t, hours, true)
		t.Logf("executions with borrowing (total %d): %v", total, executions)

		for _, spec := range starvationOrg() {
			slots := int(time.Duration(hours) * time.Hour / spec.period)
			require.GreaterOrEqual(t, executions[spec.name], slots/2,
				"%s must run at least every other period: %v", spec.name, executions)
		}

		// SaaS cap 10/min: one burst at start plus at most one burst of debt.
		const capPerMinute = 10
		require.LessOrEqual(t, total, hours*60*capPerMinute+2*capPerMinute,
			"borrowing must not lift the org's long-run rate above its cap")
	})
}

// TestReserveDeferredCheckExecutionDebtIsBounded pins the borrowing contract: a
// deferred job may take the drained bucket down to one burst below zero, no
// further, and on-time jobs wait until refill has repaid the debt.
func TestReserveDeferredCheckExecutionDebtIsBounded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	svc := entitlements.NewService(
		dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0,
		entitlements.WithClock(func() time.Time { return now }),
	)

	// SaaS cap 10/min: drain the burst.
	for i := range 10 {
		r.NoError(svc.ReserveCheckExecution(ctx, org.UID), "call %d", i+1)
	}

	r.ErrorIs(svc.ReserveCheckExecution(ctx, org.UID), entitlements.ErrEntitlementExceeded)

	for i := range 10 {
		r.NoError(svc.ReserveDeferredCheckExecution(ctx, org.UID), "borrow %d", i+1)
	}

	r.ErrorIs(svc.ReserveDeferredCheckExecution(ctx, org.UID), entitlements.ErrEntitlementExceeded,
		"the debt stops at one burst")

	// One minute refills 10 tokens: the debt is repaid, the bucket is at 0.
	now = now.Add(time.Minute)
	r.ErrorIs(svc.ReserveCheckExecution(ctx, org.UID), entitlements.ErrEntitlementExceeded,
		"on-time jobs wait until the debt is repaid")

	now = now.Add(7 * time.Second)
	r.NoError(svc.ReserveCheckExecution(ctx, org.UID), "refill after the debt serves on-time jobs again")
}
