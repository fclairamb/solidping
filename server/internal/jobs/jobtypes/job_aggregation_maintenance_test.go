package jobtypes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// rawResultMaintenance builds a raw result tagged as recorded under an active
// maintenance window (spec 2026-08-20-01).
func rawResultMaintenance(
	status models.ResultStatus, offset time.Duration, periodStart time.Time,
) *models.Result {
	result := rawResultWithStatus(status, offset, periodStart)
	result.Maintenance = true

	return result
}

// TestAggregateMaintenanceCountersAreSubsets pins the invariant the whole SLO
// exclusion rests on: the maintenance counters are SUBSETS of total_checks /
// successful_checks, never replacements. If they were replacements, every
// existing consumer (status pages, badges, the availability API) would silently
// change its numbers the day tagging shipped.
func TestAggregateMaintenanceCountersAreSubsets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)

	results := []*models.Result{
		rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusUp, 2*time.Minute, periodStart),
		rawResultMaintenance(models.ResultStatusUp, 3*time.Minute, periodStart),
		rawResultMaintenance(models.ResultStatusDown, 4*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodEnd)

	r.NotNil(agg.TotalChecks)
	r.Equal(4, *agg.TotalChecks, "maintenance probes stay in total_checks")
	r.NotNil(agg.SuccessfulChecks)
	r.Equal(3, *agg.SuccessfulChecks, "a successful maintenance probe stays in successful_checks")

	r.NotNil(agg.MaintenanceChecks)
	r.Equal(2, *agg.MaintenanceChecks)
	r.NotNil(agg.MaintenanceSuccessfulChecks)
	r.Equal(1, *agg.MaintenanceSuccessfulChecks)

	// The subset invariant, stated as an assertion.
	r.LessOrEqual(*agg.MaintenanceChecks, *agg.TotalChecks)
	r.LessOrEqual(*agg.MaintenanceSuccessfulChecks, *agg.SuccessfulChecks)
	r.LessOrEqual(*agg.MaintenanceSuccessfulChecks, *agg.MaintenanceChecks)
}

// A window with no maintenance at all must report zero, not nil — a rollup
// written after tagging shipped knows the answer and should say so.
func TestAggregateMaintenanceZeroWhenNoneTagged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	results := []*models.Result{
		rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusDown, 2*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodStart.Add(time.Hour))

	// Positive control first: the fixture really did reach the calculation.
	r.NotNil(agg.TotalChecks)
	r.Equal(2, *agg.TotalChecks)

	r.NotNil(agg.MaintenanceChecks)
	r.Zero(*agg.MaintenanceChecks)
	r.NotNil(agg.MaintenanceSuccessfulChecks)
	r.Zero(*agg.MaintenanceSuccessfulChecks)
}

// A lifecycle marker or an abandoned attempt must not enter the maintenance
// counters either — they are subsets of counters those rows are already
// excluded from, so counting them would break the subset invariant.
func TestAggregateMaintenanceExcludesNonMeasurements(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	results := []*models.Result{
		rawResultMaintenance(models.ResultStatusUp, 1*time.Minute, periodStart),
		rawResultMaintenance(models.ResultStatusAbandoned, 2*time.Minute, periodStart),
		rawResultMaintenance(models.ResultStatusCreated, 3*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodStart.Add(time.Hour))

	r.NotNil(agg.TotalChecks)
	r.Equal(1, *agg.TotalChecks)
	r.NotNil(agg.MaintenanceChecks)
	r.Equal(1, *agg.MaintenanceChecks, "only the countable maintenance probe is counted")
	r.LessOrEqual(*agg.MaintenanceChecks, *agg.TotalChecks)
}

// TestAggregateMaintenanceSurvivesTierChain is the spec's required test: the
// counters must survive raw -> hour -> day -> month and never double-count.
// The tiers are disjoint by construction (the aggregation job compacts and
// deletes in one transaction), so a month row's counters must equal the exact
// raw probe counts it descends from.
func TestAggregateMaintenanceSurvivesTierChain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Three hours of raw probes. Hour 0: 4 probes, 1 maintenance (successful).
	// Hour 1: 4 probes, 2 maintenance (1 successful, 1 down).
	// Hour 2: 4 probes, 0 maintenance.
	hourSpecs := [][]*models.Result{
		{
			rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, base),
			rawResultWithStatus(models.ResultStatusUp, 2*time.Minute, base),
			rawResultWithStatus(models.ResultStatusDown, 3*time.Minute, base),
			rawResultMaintenance(models.ResultStatusUp, 4*time.Minute, base),
		},
		{
			rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, base.Add(time.Hour)),
			rawResultWithStatus(models.ResultStatusUp, 2*time.Minute, base.Add(time.Hour)),
			rawResultMaintenance(models.ResultStatusUp, 3*time.Minute, base.Add(time.Hour)),
			rawResultMaintenance(models.ResultStatusDown, 4*time.Minute, base.Add(time.Hour)),
		},
		{
			rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, base.Add(2*time.Hour)),
			rawResultWithStatus(models.ResultStatusUp, 2*time.Minute, base.Add(2*time.Hour)),
			rawResultWithStatus(models.ResultStatusUp, 3*time.Minute, base.Add(2*time.Hour)),
			rawResultWithStatus(models.ResultStatusWarning, 4*time.Minute, base.Add(2*time.Hour)),
		},
	}

	const (
		wantTotal        = 12
		wantSuccess      = 10 // 12 - 2 down
		wantMaintTotal   = 3
		wantMaintSuccess = 2
	)

	hourRows := make([]*models.Result, 0, len(hourSpecs))

	for i, raws := range hourSpecs {
		hourStart := base.Add(time.Duration(i) * time.Hour)
		hourRows = append(hourRows, aggregateResults(raws, models.PeriodTypeHour, hourStart, hourStart.Add(time.Hour)))
	}

	// Positive control: the hour tier really did receive the tags.
	r.NotNil(hourRows[1].MaintenanceChecks)
	r.Equal(2, *hourRows[1].MaintenanceChecks)

	dayRow := aggregateResults(hourRows, models.PeriodTypeDay, base, base.AddDate(0, 0, 1))
	monthRow := aggregateResults(
		[]*models.Result{dayRow}, models.PeriodTypeMonth, base, base.AddDate(0, 1, 0),
	)

	for tier, row := range map[string]*models.Result{"day": dayRow, "month": monthRow} {
		r.NotNil(row.TotalChecks, tier)
		r.Equal(wantTotal, *row.TotalChecks, tier)
		r.NotNil(row.SuccessfulChecks, tier)
		r.Equal(wantSuccess, *row.SuccessfulChecks, tier)
		r.NotNil(row.MaintenanceChecks, tier)
		r.Equal(wantMaintTotal, *row.MaintenanceChecks, tier)
		r.NotNil(row.MaintenanceSuccessfulChecks, tier)
		r.Equal(wantMaintSuccess, *row.MaintenanceSuccessfulChecks, tier)
	}
}

// A child rolled up BEFORE maintenance tagging shipped carries nil counters.
// Those must contribute nothing rather than being read as a confident zero —
// and, crucially, must not corrupt the counters of siblings that do carry them.
func TestAggregateMaintenanceNilChildContributesNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tagged := aggregateResults([]*models.Result{
		rawResultMaintenance(models.ResultStatusUp, 1*time.Minute, base),
		rawResultWithStatus(models.ResultStatusUp, 2*time.Minute, base),
	}, models.PeriodTypeHour, base, base.Add(time.Hour))

	legacy := aggregateResults([]*models.Result{
		rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, base.Add(time.Hour)),
		rawResultWithStatus(models.ResultStatusUp, 2*time.Minute, base.Add(time.Hour)),
	}, models.PeriodTypeHour, base.Add(time.Hour), base.Add(2*time.Hour))
	// Simulate a pre-tagging rollup row.
	legacy.MaintenanceChecks = nil
	legacy.MaintenanceSuccessfulChecks = nil

	dayRow := aggregateResults(
		[]*models.Result{tagged, legacy}, models.PeriodTypeDay, base, base.AddDate(0, 0, 1),
	)

	r.NotNil(dayRow.TotalChecks)
	r.Equal(4, *dayRow.TotalChecks)
	r.NotNil(dayRow.MaintenanceChecks)
	r.Equal(1, *dayRow.MaintenanceChecks, "the nil child adds nothing, the tagged child keeps its 1")
}
