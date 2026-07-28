package jobtypes

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// TestAggregatePeriod_CompactsFinalMillisecondRow is the regression test for the
// spec 2026-07-22-03 diagnosis. A raw row whose period_start is exactly the
// final millisecond of its hour (HH:59:59.999) used to be discovered by
// work-discovery but excluded from its own bucket's targeted fetch — the fetch
// bounded period_start < periodEnd with periodEnd = HH:59:59.999, so the row on
// that exact instant was dropped, the fetch came back empty, and the row was
// re-discovered forever and never rolled up (the stranded window). With the
// exclusive next-bucket fetch bound (periodEnd + 1ms) it is compacted normally.
func TestAggregatePeriod_CompactsFinalMillisecondRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	oldBucket := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	// The last representable millisecond of the hour: HH:59:59.999 — exactly the
	// old inclusive periodEnd, which the strict `period_start < periodEnd` fetch
	// used to exclude.
	boundaryRow := seedRawResult(
		t, dbSvc, org, check, models.ResultStatusUp, oldBucket.Add(time.Hour).Add(-time.Millisecond),
	)

	run := &AggregationJobRun{}
	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	aggregated, err := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.True(aggregated, "a raw row on the final millisecond of the hour must be compacted, not stranded")

	// The boundary-millisecond raw row is gone, rolled into exactly one hour row.
	_, err = dbSvc.GetResult(ctx, boundaryRow.UID)
	r.Error(err, "the boundary-millisecond raw row must be deleted after rollup")

	hourRows := listHourRows(t, dbSvc, org, check)
	r.Len(hourRows, 1, "the final-millisecond row must roll up into exactly one hour bucket")
	r.WithinDuration(oldBucket, hourRows[0].PeriodStart, time.Second, "the rollup must land in the HH:00 bucket")
	r.NotNil(hourRows[0].TotalChecks)
	r.Equal(1, *hourRows[0].TotalChecks)
}
