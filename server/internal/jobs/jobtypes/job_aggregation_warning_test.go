package jobtypes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// rawResultWithStatus builds a minimal raw result fixture carrying the given
// status and a fixed duration, ordered by the supplied offset.
func rawResultWithStatus(status models.ResultStatus, offset time.Duration, periodStart time.Time) *models.Result {
	s := int(status)
	d := float32(100.0)
	region := testRegion
	worker := testWorkerUID

	return &models.Result{
		UID:             "r-" + offset.String(),
		OrganizationUID: testOrgUID,
		CheckUID:        testCheckUID,
		Region:          &region,
		WorkerUID:       &worker,
		Status:          &s,
		Duration:        &d,
		PeriodStart:     periodStart.Add(offset),
		Output:          models.JSONMap{"msg": "x"},
	}
}

// TestAggregateAvailabilityUpAndWarningIsHundredPctDegraded pins Decision A +
// the promotion rule b: a window of Up + Warning yields availability_pct == 100
// (Warning counts as up) AND the aggregated row status is Degraded (the window
// contained a warning, no dominating failure).
func TestAggregateAvailabilityUpAndWarningIsHundredPctDegraded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)

	results := []*models.Result{
		rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusUp, 2*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusWarning, 3*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodEnd)

	// Availability (100%: warning counts as up) is derived at read time from
	// successful_checks / total_checks, both stored on the row.
	r.NotNil(agg.TotalChecks)
	r.Equal(3, *agg.TotalChecks, "all three are countable")
	r.NotNil(agg.SuccessfulChecks)
	r.Equal(3, *agg.SuccessfulChecks, "all three count as successful")
	r.NotNil(agg.Status)
	r.Equal(int(models.ResultStatusDegraded), *agg.Status,
		"a non-failing window containing a warning must roll up to Degraded")
}

// TestAggregateSeverityDownAndWarningRollsUpToDown pins Decision D: a window
// with a dominating Down + a Warning rolls up to Down, never Degraded/Warning.
func TestAggregateSeverityDownAndWarningRollsUpToDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)

	results := []*models.Result{
		rawResultWithStatus(models.ResultStatusDown, 1*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusDown, 2*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusWarning, 3*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodEnd)

	r.NotNil(agg.Status)
	r.Equal(int(models.ResultStatusDown), *agg.Status,
		"a dominating failure must win over a warning")
}

// TestAggregateSeverityTieDownVsWarningResolvesToDown pins the tie-break: an
// equal-count Down vs Warning window resolves to Down via Severity(), never to
// the higher numeric Warning value.
func TestAggregateSeverityTieDownVsWarningResolvesToDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)

	// 1 Down, 1 Warning: a numeric tie-break would pick Warning(8) > Down(4);
	// Severity() must resolve the tie to the failure.
	results := []*models.Result{
		rawResultWithStatus(models.ResultStatusDown, 1*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusWarning, 2*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodEnd)

	r.NotNil(agg.Status)
	r.Equal(int(models.ResultStatusDown), *agg.Status,
		"a Down/Warning tie must resolve to Down, never to Warning")
}

// TestCalculateDominantStatusPromotesWarning unit-tests the rollup selector
// directly across the key cases.
func TestCalculateDominantStatusPromotesWarning(t *testing.T) {
	t.Parallel()

	up := int(checkerdef.StatusUp)
	down := int(checkerdef.StatusDown)
	warning := int(checkerdef.StatusWarning)
	degraded := int(checkerdef.StatusDegraded)

	tests := []struct {
		name   string
		counts map[int]int
		want   int
	}{
		{"all up", map[int]int{up: 5}, up},
		{"up plus warning promotes to degraded", map[int]int{up: 5, warning: 1}, degraded},
		{"only warning promotes to degraded", map[int]int{warning: 3}, degraded},
		{"dominating down wins", map[int]int{down: 4, warning: 2}, down},
		{"down vs warning tie resolves to down", map[int]int{down: 1, warning: 1}, down},
		{"degraded child carries to degraded", map[int]int{up: 10, degraded: 1}, degraded},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, calculateDominantStatus(tc.counts))
		})
	}
}

// TestStatusSeverityRanking pins the gravity ordering used by the rollup.
func TestStatusSeverityRanking(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Failures outrank Degraded outranks Up; Warning ranks at Up level.
	r.Greater(checkerdef.StatusDown.Severity(), checkerdef.StatusDegraded.Severity())
	r.Greater(checkerdef.StatusTimeout.Severity(), checkerdef.StatusDegraded.Severity())
	r.Greater(checkerdef.StatusError.Severity(), checkerdef.StatusDegraded.Severity())
	r.Greater(checkerdef.StatusDegraded.Severity(), checkerdef.StatusUp.Severity())
	r.Equal(checkerdef.StatusUp.Severity(), checkerdef.StatusWarning.Severity(),
		"warning ranks at up level (it counts as up for availability)")
}
