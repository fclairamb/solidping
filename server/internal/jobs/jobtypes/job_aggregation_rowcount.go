package jobtypes

import (
	"context"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// resultsRowCountSampleInterval bounds how often the aggregation job refreshes
// solidping_results_rows. The gauge is table-wide (every organization),
// so it must not be recomputed on every single org's aggregation run — that
// would multiply an already-expensive table-wide COUNT(*) by however many
// orgs run the job in the interval. Tying the refresh to this job (rather
// than per-request or a brand new dedicated ticker) keeps "the count is not
// free" honest: it only ever runs on the existing background cadence
// (spec 2026-08-17-04 §3).
const resultsRowCountSampleInterval = 5 * time.Minute

// resultsRowCountThrottle process-wide throttles the table-wide sample below.
//
// are created fresh per invocation, so the "last sampled" state cannot live on
// the (per-run) receiver.
//
//nolint:gochecknoglobals // Process-wide throttle: AggregationJobRun instances
var resultsRowCountThrottle struct {
	mu   sync.Mutex
	last time.Time
}

// sampleResultsRowCountFunc is swappable in tests to observe/skip the real DB
// query without a live database.
var sampleResultsRowCountFunc = defaultSampleResultsRowCount //nolint:gochecknoglobals // test seam

// maybeSampleResultsRowCount refreshes the results-row-count gauge at most
// once per resultsRowCountSampleInterval, process-wide. Errors are logged and
// otherwise ignored — a stale/missing gauge sample must never fail or delay
// the aggregation job itself.
func maybeSampleResultsRowCount(ctx context.Context, jctx *jobdef.JobContext) {
	resultsRowCountThrottle.mu.Lock()
	due := time.Since(resultsRowCountThrottle.last) >= resultsRowCountSampleInterval
	if due {
		resultsRowCountThrottle.last = time.Now()
	}
	resultsRowCountThrottle.mu.Unlock()

	if !due {
		return
	}

	sampleResultsRowCountFunc(ctx, jctx)
}

func defaultSampleResultsRowCount(ctx context.Context, jctx *jobdef.JobContext) {
	counts, err := jctx.DBService.CountResultsByPeriodType(ctx)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to sample results row count", "error", err)

		return
	}

	// Zero-fill every known period_type so a drained tier drops to 0 rather
	// than going stale/absent on the dashboard.
	for _, periodType := range []string{periodRaw, periodHour, periodDay, periodMonth} {
		prommetrics.SetResultsRowCount(periodType, float64(counts[periodType]))
	}
}
