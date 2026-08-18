package jobtypes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// rawResultAbandoned builds a raw result fixture the abandoned-result reaper
// would have produced: the dedicated terminal models.ResultStatusAbandoned
// (spec 2026-08-18-10, which replaced the `abandoned` boolean column).
func rawResultAbandoned(offset time.Duration, periodStart time.Time) *models.Result {
	return rawResultWithStatus(models.ResultStatusAbandoned, offset, periodStart)
}

// TestAggregateAbandonedExcludedGenuineErrorCounts is the required test for
// spec 2026-08-18-03: a reaped/abandoned attempt must not move the hour
// rollup's availability percentage at all — it must be absent from BOTH
// total_checks and successful_checks, not merely fail to count as success —
// while a genuine error result in the SAME window still drags availability
// down. The positive control is load-bearing: a fixture that never reached
// the calculation would trivially pass an "unchanged" assertion on its own.
func TestAggregateAbandonedExcludedGenuineErrorCounts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)

	// One genuine Up and one reaped/abandoned attempt. If the abandoned row
	// were counted, this would read 50% (1/2); if it silently vanished from
	// total_checks entirely while still being iterated, that would also be
	// wrong in a different way (a nil TotalChecks or a panic). The correct
	// reading is 100% availability over exactly 1 countable check.
	results := []*models.Result{
		rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, periodStart),
		rawResultAbandoned(2*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodEnd)

	r.NotNil(agg.TotalChecks)
	r.Equal(1, *agg.TotalChecks, "the abandoned row must not enter total_checks")
	r.NotNil(agg.SuccessfulChecks)
	r.Equal(1, *agg.SuccessfulChecks, "the up row is the only success; the abandoned row contributes nothing")

	// Positive control: replace the abandoned row with a GENUINE
	// ResultStatusError in the same shape of window and confirm it DOES drag
	// availability down — proving the exclusion is specific to
	// ResultStatusAbandoned, not a bug that drops errors from the count in
	// general.
	controlResults := []*models.Result{
		rawResultWithStatus(models.ResultStatusUp, 1*time.Minute, periodStart),
		rawResultWithStatus(models.ResultStatusError, 2*time.Minute, periodStart),
	}

	controlAgg := aggregateResults(controlResults, models.PeriodTypeHour, periodStart, periodEnd)

	r.NotNil(controlAgg.TotalChecks)
	r.Equal(2, *controlAgg.TotalChecks, "a genuine error must enter total_checks")
	r.NotNil(controlAgg.SuccessfulChecks)
	r.Equal(1, *controlAgg.SuccessfulChecks, "a genuine error must not count as successful")
}

// TestAggregateAllAbandonedBucketReadsAsNoData covers the edge the exclusion
// rule creates: an hour whose only raw rows are abandoned reads as "no data"
// (TotalChecks == 0), not as a phantom 0% outage manufactured from our own
// worker's crash.
func TestAggregateAllAbandonedBucketReadsAsNoData(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(time.Hour)

	results := []*models.Result{
		rawResultAbandoned(1*time.Minute, periodStart),
		rawResultAbandoned(2*time.Minute, periodStart),
	}

	agg := aggregateResults(results, models.PeriodTypeHour, periodStart, periodEnd)

	r.NotNil(agg.TotalChecks)
	r.Equal(0, *agg.TotalChecks, "an all-abandoned window must read as no data, not as measured downtime")
	r.NotNil(agg.SuccessfulChecks)
	r.Equal(0, *agg.SuccessfulChecks)
}
