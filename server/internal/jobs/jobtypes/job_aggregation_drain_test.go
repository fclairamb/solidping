package jobtypes

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// seedRawBacklog seeds one measurable raw row in each of `hours` consecutive
// hour buckets, all past the default raw retention.
func seedRawBacklog(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, check *models.Check, hours int,
) {
	t.Helper()

	oldest := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	for i := range hours {
		seedRawResult(t, dbSvc, org, check, models.ResultStatusUp,
			oldest.Add(-time.Duration(i)*time.Hour).Add(5*time.Minute))
	}
}

func countRawRows(t *testing.T, dbSvc *sqlite.Service, org *models.Organization, check *models.Check) int {
	t.Helper()

	resp, err := dbSvc.ListResults(t.Context(), &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{periodRaw},
		Limit:           100,
	})
	require.New(t).NoError(err)

	return len(resp.Results)
}

// TestRun_DrainsWholeBacklogInOneRun pins that one aggregation run rolls up
// every ready bucket, not a single one: an org with many check × region pairs
// used to need one job round-trip per bucket, and queue latency let the raw
// backlog outgrow the raw retention.
func TestRun_DrainsWholeBacklogInOneRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbSvc, org, check := newAggTestEnv(t)

	const backlog = 5
	seedRawBacklog(t, dbSvc, org, check, backlog)

	jobs := &recordingJobService{}
	jctx := &jobdef.JobContext{
		OrganizationUID: &org.UID,
		DBService:       dbSvc,
		Services:        &services.Registry{Jobs: jobs},
		Logger:          slog.Default(),
	}

	r.NoError((&AggregationJobRun{}).Run(t.Context(), jctx))

	r.Len(listHourRows(t, dbSvc, org, check), backlog, "every ready raw bucket must be rolled up in one run")
	r.Zero(countRawRows(t, dbSvc, org, check), "no aggregatable raw row may be left behind")

	r.Len(jobs.scheduledAt, 1)
	r.WithinDuration(time.Now().Add(1*time.Hour), jobs.scheduledAt[0], 2*time.Minute,
		"a drained run reschedules ~1h out")
}

// TestRun_BucketCapReschedulesImmediately pins that a run stopping on its
// bucket cap (or time budget) with work left reschedules at delay=0, and logs
// the backlog at INFO.
func TestRun_BucketCapReschedulesImmediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbSvc, org, check := newAggTestEnv(t)

	seedRawBacklog(t, dbSvc, org, check, 4)

	var logBuf bytes.Buffer

	jobs := &recordingJobService{}
	jctx := &jobdef.JobContext{
		OrganizationUID: &org.UID,
		DBService:       dbSvc,
		Services:        &services.Registry{Jobs: jobs},
		Logger:          slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	r.NoError((&AggregationJobRun{maxBuckets: 2}).Run(t.Context(), jctx))

	r.Len(listHourRows(t, dbSvc, org, check), 2, "the run must stop at its bucket cap")
	r.Equal(2, countRawRows(t, dbSvc, org, check))

	r.Len(jobs.scheduledAt, 1)
	r.WithinDuration(time.Now(), jobs.scheduledAt[0], 2*time.Minute,
		"a run that stopped with work left must reschedule immediately")

	r.Contains(logBuf.String(), "budget_exhausted=true")
}

// TestLogAggregationRun_ReportsLagAtInfo pins the visibility half: a run whose
// oldest raw bucket is hours past the retention boundary logs at INFO with the
// lag, while an on-time run stays out of INFO.
func TestLogAggregationRun_ReportsLagAtInfo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var logBuf bytes.Buffer

	jctx := &jobdef.JobContext{
		Logger: slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	boundary, err := calculateAggregationBoundary(periodRaw, defaultRetentionRawHours, 1, 1)
	r.NoError(err)

	// On time: the bucket just before the boundary.
	logAggregationRun(t.Context(), jctx, &aggregationRunStats{
		buckets:        map[string]int{periodRaw: 1},
		oldestRawStart: boundary.Add(-time.Hour),
	}, nil)
	r.Empty(logBuf.String(), "an on-time run must not log at INFO")

	// Three hours late.
	logAggregationRun(t.Context(), jctx, &aggregationRunStats{
		buckets:        map[string]int{periodRaw: 3},
		oldestRawStart: boundary.Add(-4 * time.Hour),
	}, nil)
	r.Contains(logBuf.String(), "Aggregation is behind its raw retention")
	r.Contains(logBuf.String(), "raw_lag_minutes=180")
}

// TestLogAggregationRun_BudgetWithoutLagIsNotBehind: a run that ran out of
// budget with no lag past the threshold says it stopped on its budget, not
// that it is behind its raw retention; with lag, the "behind" message wins.
func TestLogAggregationRun_BudgetWithoutLagIsNotBehind(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var logBuf bytes.Buffer

	jctx := &jobdef.JobContext{
		Logger: slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	boundary, err := calculateAggregationBoundary(periodRaw, defaultRetentionRawHours, 1, 1)
	r.NoError(err)

	// Out of budget, no raw lag at all (only hour buckets rolled up).
	logAggregationRun(t.Context(), jctx, &aggregationRunStats{
		buckets:         map[string]int{periodHour: 64},
		duration:        30 * time.Second,
		budgetExhausted: true,
	}, nil)

	// Out of budget, the oldest raw bucket within the threshold.
	logAggregationRun(t.Context(), jctx, &aggregationRunStats{
		buckets:         map[string]int{periodRaw: 64},
		oldestRawStart:  boundary.Add(-90 * time.Minute),
		budgetExhausted: true,
	}, nil)

	out := logBuf.String()
	r.NotContains(out, "Aggregation is behind its raw retention")
	r.Equal(2, strings.Count(out, "Aggregation stopped on its run budget"))
	r.Contains(out, "duration_ms=30000")

	// Out of budget AND hours late: behind.
	logBuf.Reset()
	logAggregationRun(t.Context(), jctx, &aggregationRunStats{
		buckets:         map[string]int{periodRaw: 3},
		oldestRawStart:  boundary.Add(-4 * time.Hour),
		budgetExhausted: true,
	}, nil)
	r.Contains(logBuf.String(), "Aggregation is behind its raw retention")
	r.NotContains(logBuf.String(), "stopped on its run budget")
}
