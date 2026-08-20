package slo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// A 30-day UTC month, fully elapsed, is the reference frame for most cases.
func juneWindow() slo.Window {
	return slo.MonthWindow(time.UTC, time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
}

func TestComputeBudgetTable(t *testing.T) {
	t.Parallel()

	window := juneWindow()
	afterWindow := window.End.Add(time.Hour)
	monthSeconds := int64(30 * 24 * 3600)

	tests := []struct {
		name  string
		in    slo.Input
		check func(*require.Assertions, slo.Status)
	}{
		{
			// The negative control the spec names explicitly: an SLO over a
			// check with zero results reports NULL attainment and an untouched
			// budget. Not 100%, not 0%.
			name: "no data is not one hundred percent",
			in: slo.Input{
				TargetPct: 99.9,
				Stats:     uptimebar.BucketStats{},
				Window:    window,
				Now:       afterWindow,
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.Nil(got.AttainmentPct)
				r.False(got.HasData)
				r.Equal(slo.StateUnknown, got.State)
				r.Zero(got.BudgetConsumedSeconds)
				// Untouched: remaining == total, and total is a real number
				// (positive control — asserting "consumed is zero" would pass
				// on an all-zero struct too).
				r.Positive(got.BudgetTotalSeconds)
				r.Equal(got.BudgetTotalSeconds, got.BudgetRemainingSeconds)
				r.Nil(got.BurnRate)
				r.Nil(got.ProjectedExhaustionAt)
			},
		},
		{
			name: "perfect month leaves the whole budget",
			in: slo.Input{
				TargetPct: 99.9,
				Stats:     uptimebar.BucketStats{Up: 1000, Total: 1000},
				Window:    window,
				Now:       afterWindow,
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.NotNil(got.AttainmentPct)
				r.InDelta(100.0, *got.AttainmentPct, 0.0001)
				r.Equal(slo.StateHealthy, got.State)
				r.Zero(got.BudgetConsumedSeconds)
				r.InDelta(float64(monthSeconds)*0.001, float64(got.BudgetTotalSeconds), 1)
				r.NotNil(got.BurnRate)
				r.InDelta(0.0, *got.BurnRate, 0.0001)
			},
		},
		{
			name: "exactly on target spends exactly the budget",
			in: slo.Input{
				TargetPct: 99.9,
				Stats:     uptimebar.BucketStats{Up: 999, Total: 1000},
				Window:    window,
				Now:       afterWindow,
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.InDelta(99.9, *got.AttainmentPct, 0.0001)
				r.InDelta(1.0, *got.BurnRate, 0.0001)
				r.Equal(got.BudgetTotalSeconds, got.BudgetConsumedSeconds)
				r.Zero(got.BudgetRemainingSeconds)
				// Burn rate of exactly 1.0 is on-target, not at risk.
				r.Equal(slo.StateHealthy, got.State)
			},
		},
		{
			name: "over target is breached",
			in: slo.Input{
				TargetPct: 99.9,
				Stats:     uptimebar.BucketStats{Up: 990, Total: 1000},
				Window:    window,
				Now:       afterWindow,
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.InDelta(99.0, *got.AttainmentPct, 0.0001)
				r.Equal(slo.StateBreached, got.State)
				r.Negative(got.BudgetRemainingSeconds)
				r.InDelta(10.0, *got.BurnRate, 0.0001)
				// Already spent: nothing left to project.
				r.Nil(got.ProjectedExhaustionAt)
			},
		},
		{
			name: "burning fast mid window is at risk, not breached",
			in: slo.Input{
				TargetPct: 99.9,
				// 0.3% failures = 3x the allowance...
				Stats:  uptimebar.BucketStats{Up: 997, Total: 1000},
				Window: window,
				// ...but only a fifth of the month has elapsed, so the month's
				// budget is not spent yet.
				Now: window.Start.Add(6 * 24 * time.Hour),
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.InDelta(3.0, *got.BurnRate, 0.0001)
				r.Equal(slo.StateAtRisk, got.State)
				r.Positive(got.BudgetRemainingSeconds)
				r.True(got.Partial)
				r.NotNil(got.ProjectedExhaustionAt)
				r.True(got.ProjectedExhaustionAt.Before(window.End))
				r.True(got.ProjectedExhaustionAt.After(window.Start.Add(6 * 24 * time.Hour)))
			},
		},
		{
			// Partial window via CoverageStart: a check created on the 21st has
			// only ~10 days of monitorable month, so its budget is ~1/3 of a
			// full month's.
			name: "coverage start shrinks the monitored window",
			in: slo.Input{
				TargetPct:     99.9,
				Stats:         uptimebar.BucketStats{Up: 1000, Total: 1000},
				Window:        window,
				Now:           afterWindow,
				CoverageStart: window.Start.Add(20 * 24 * time.Hour),
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.Equal(int64(10*24*3600), got.MonitoredSeconds)
				r.Equal(got.MonitoredSeconds, got.ElapsedSeconds)
				r.InDelta(float64(10*24*3600)*0.001, float64(got.BudgetTotalSeconds), 1)
				r.True(got.Partial)
			},
		},
		{
			name: "coverage start after the window yields nothing monitorable",
			in: slo.Input{
				TargetPct:     99.9,
				Stats:         uptimebar.BucketStats{},
				Window:        window,
				Now:           afterWindow,
				CoverageStart: window.End.Add(24 * time.Hour),
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.Zero(got.MonitoredSeconds)
				r.Zero(got.ElapsedSeconds)
				r.Zero(got.BudgetTotalSeconds)
				r.Equal(slo.StateUnknown, got.State)
			},
		},
		{
			// A 100% target has no allowance to divide by, so there is no
			// meaningful burn rate — but a single failure is still a breach.
			name: "hundred percent target has no burn rate",
			in: slo.Input{
				TargetPct: 100,
				Stats:     uptimebar.BucketStats{Up: 999, Total: 1000},
				Window:    window,
				Now:       afterWindow,
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.Nil(got.BurnRate)
				r.Zero(got.BudgetTotalSeconds)
				r.Positive(got.BudgetConsumedSeconds)
				r.Equal(slo.StateBreached, got.State)
			},
		},
		{
			name: "hundred percent target with a perfect month is healthy",
			in: slo.Input{
				TargetPct: 100,
				Stats:     uptimebar.BucketStats{Up: 1000, Total: 1000},
				Window:    window,
				Now:       afterWindow,
			},
			check: func(r *require.Assertions, got slo.Status) {
				r.Zero(got.BudgetTotalSeconds)
				r.Zero(got.BudgetConsumedSeconds)
				r.Equal(slo.StateHealthy, got.State)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(require.New(t), slo.Compute(tt.in))
		})
	}
}

// Planned maintenance must not cost error budget. A 2h planned window on a
// 99.9% monthly objective is ~4.6x the entire month's allowance, which is
// exactly what made SLOs untrustworthy before ingest-time tagging.
func TestComputeMaintenanceSubtraction(t *testing.T) {
	t.Parallel()

	window := juneWindow()
	afterWindow := window.End.Add(time.Hour)

	// 1000 probes, 100 of them under maintenance and all 100 of those failed.
	stats := uptimebar.BucketStats{
		Up: 900, Total: 1000,
		MaintUp: 0, MaintTotal: 100,
	}

	t.Run("excluded", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		got := slo.Compute(slo.Input{
			TargetPct: 99.9, Stats: stats, Window: window, Now: afterWindow,
			ExcludeMaintenance: true,
		})

		// The 900 remaining probes were all successful.
		r.NotNil(got.AttainmentPct)
		r.InDelta(100.0, *got.AttainmentPct, 0.0001)
		r.Equal(900, got.TotalChecks)
		r.Equal(slo.StateHealthy, got.State)
		r.Zero(got.BudgetConsumedSeconds)
		// 10% of the month was maintenance, and it is reported explicitly.
		r.InDelta(float64(30*24*3600)*0.1, float64(got.ExcludedMaintenanceSeconds), 2)
		// The month's own allowance shrinks with it.
		r.InDelta(float64(30*24*3600)*0.9*0.001, float64(got.BudgetTotalSeconds), 2)
	})

	t.Run("not excluded", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		got := slo.Compute(slo.Input{
			TargetPct: 99.9, Stats: stats, Window: window, Now: afterWindow,
			ExcludeMaintenance: false,
		})

		// Positive control for the negative above: with the flag off the SAME
		// input is a hard breach, so the exclusion is really doing the work.
		r.InDelta(90.0, *got.AttainmentPct, 0.0001)
		r.Equal(1000, got.TotalChecks)
		r.Equal(slo.StateBreached, got.State)
		// Still reported, so a partially-covered month stays legible.
		r.Positive(got.ExcludedMaintenanceSeconds)
	})
}

// The subset invariant is enforced by the schema's semantics, not by a DB
// constraint. If a corrupt row ever violated it, the clamp must degrade to
// "exclusion did nothing" rather than produce a negative denominator.
func TestExcludingMaintenanceClampsOversizedSubsets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := uptimebar.BucketStats{Up: 5, Total: 10, MaintUp: 50, MaintTotal: 100}.ExcludingMaintenance()
	r.Zero(got.Total)
	r.Zero(got.Up)
	r.Zero(got.MaintTotal)

	_, ok := got.AvailabilityPct()
	r.False(ok)
}

func TestMergeStatsGroupSemantics(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	a := uptimebar.BucketStats{Up: 100, Total: 100, MaintUp: 10, MaintTotal: 10}
	b := uptimebar.BucketStats{Up: 80, Total: 100}
	// A member with no rows contributes nothing at all — NOT a zero.
	empty := uptimebar.BucketStats{}

	merged := slo.MergeStats([]uptimebar.BucketStats{a, b, empty})
	r.Equal(180, merged.Up)
	r.Equal(200, merged.Total)
	r.Equal(10, merged.MaintTotal)

	// A one-member group must produce exactly what that member's own SLO does.
	single := slo.MergeStats([]uptimebar.BucketStats{a})
	r.Equal(a, single)

	window := juneWindow()
	groupStatus := slo.Compute(slo.Input{
		TargetPct: 99, Stats: merged, Window: window, Now: window.End.Add(time.Hour),
	})
	r.InDelta(90.0, *groupStatus.AttainmentPct, 0.0001)

	// Weighted, not an average of percentages: (100% + 80%)/2 would be 90% too,
	// so use an uneven split to prove the weighting.
	uneven := slo.MergeStats([]uptimebar.BucketStats{
		{Up: 300, Total: 300},
		{Up: 50, Total: 100},
	})
	unevenStatus := slo.Compute(slo.Input{
		TargetPct: 99, Stats: uneven, Window: window, Now: window.End.Add(time.Hour),
	})
	r.InDelta(87.5, *unevenStatus.AttainmentPct, 0.0001)
}

// A group whose members have zero results between them must behave exactly like
// a single check with zero results.
func TestGroupWithNoResultsIsUnknown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	window := juneWindow()

	merged := slo.MergeStats([]uptimebar.BucketStats{{}, {}, {}})
	got := slo.Compute(slo.Input{
		TargetPct: 99.9, Stats: merged, Window: window, Now: window.End.Add(time.Hour),
	})

	r.Nil(got.AttainmentPct)
	r.Equal(slo.StateUnknown, got.State)
	r.Equal(got.BudgetTotalSeconds, got.BudgetRemainingSeconds)
}

// The budget must be denominated in the real length of the calendar month, so a
// DST-shortened March buys an hour less of allowance than a DST-lengthened
// October.
func TestBudgetTracksDSTMonthLength(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	paris := mustLoad(t, "Europe/Paris")

	perfect := uptimebar.BucketStats{Up: 100, Total: 100}

	spring := slo.MonthWindow(paris, time.Date(2026, 3, 10, 0, 0, 0, 0, paris))
	autumn := slo.MonthWindow(paris, time.Date(2026, 10, 10, 0, 0, 0, 0, paris))

	springStatus := slo.Compute(slo.Input{
		TargetPct: 99, Stats: perfect, Window: spring, Now: spring.End.Add(time.Hour),
	})
	autumnStatus := slo.Compute(slo.Input{
		TargetPct: 99, Stats: perfect, Window: autumn, Now: autumn.End.Add(time.Hour),
	})

	r.Equal(int64(31*24*3600-3600), springStatus.MonitoredSeconds)
	r.Equal(int64(31*24*3600+3600), autumnStatus.MonitoredSeconds)
	// Two hours of wall clock difference at a 1% allowance = 72 s of budget.
	r.Equal(int64(72), autumnStatus.BudgetTotalSeconds-springStatus.BudgetTotalSeconds)
}
