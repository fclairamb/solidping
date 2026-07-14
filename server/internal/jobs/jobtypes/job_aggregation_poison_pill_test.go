package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// poisonPillOldOffset places a bucket safely past the 24h raw retention default
// so work-discovery treats it as ready to aggregate.
const poisonPillOldOffset = -30 * time.Hour

// recordingJobService is a minimal jobsvc.Service that only records the
// scheduled-at of the jobs Run reschedules. Every other method is inherited
// from the embedded nil interface and panics if called — Run only calls
// CreateJob.
type recordingJobService struct {
	jobsvc.Service
	scheduledAt []time.Time
}

var _ jobsvc.Service = (*recordingJobService)(nil)

func (s *recordingJobService) CreateJob(
	_ context.Context, _, _ string, _ json.RawMessage, jobOptions *jobsvc.JobOptions,
) (*models.Job, error) {
	when := time.Now()
	if jobOptions != nil && jobOptions.ScheduledAt != nil {
		when = *jobOptions.ScheduledAt
	}

	s.scheduledAt = append(s.scheduledAt, when)

	return &models.Job{UID: uuid.Must(uuid.NewV7()).String()}, nil
}

// newAggTestEnv builds an in-memory SQLite service with one organization and one
// check, and strips the "created" marker CreateCheck auto-seeds at wall-clock
// now so the synthetic old buckets each test seeds are the only rows present.
func newAggTestEnv(t *testing.T) (*sqlite.Service, *models.Organization, *models.Check) {
	t.Helper()

	require := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(err)
	require.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("agg-pp-org", "")
	require.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "agg-pp-check", "http")
	require.NoError(dbSvc.CreateCheck(ctx, check))

	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           100,
	})
	require.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		require.NoError(delErr)
	}

	return dbSvc, org, check
}

// seedRawResult inserts one raw result for the check at the given period_start.
func seedRawResult(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, check *models.Check,
	status models.ResultStatus, when time.Time,
) *models.Result {
	t.Helper()

	res := models.NewResult(org.UID, check.UID, status, 0.1)
	res.PeriodStart = when
	require.New(t).NoError(dbSvc.CreateResult(t.Context(), res))

	return res
}

func listHourRows(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, check *models.Check,
) []*models.Result {
	t.Helper()

	resp, err := dbSvc.ListResults(t.Context(), &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{periodHour},
		Limit:           100,
	})
	require.New(t).NoError(err)

	return resp.Results
}

// TestFindAggregatableResults_SkipsMarkerOnlyBucket pins spec 2026-07-11-16 §1:
// a bucket whose only raw rows are lifecycle markers (created/running) must not
// be discovered as aggregatable — it is the poison pill's food source.
func TestFindAggregatableResults_SkipsMarkerOnlyBucket(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	oldBucket := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	seedRawResult(t, dbSvc, org, check, models.ResultStatusCreated, oldBucket.Add(1*time.Minute))
	seedRawResult(t, dbSvc, org, check, models.ResultStatusRunning, oldBucket.Add(2*time.Minute))

	run := &AggregationJobRun{}
	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	checkUID, _, _, found, err := run.findAggregatableResults(ctx, jctx, org.UID, periodRaw)
	r.NoError(err)
	r.False(found, "a marker-only bucket must never be selected by work discovery")
	r.Empty(checkUID, "no check should be returned when nothing is aggregatable")
}

// TestFindAggregatableResults_FindsMeasurableBucket is the control: a bucket
// that also holds a measurable row is still discovered (via that row).
func TestFindAggregatableResults_FindsMeasurableBucket(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	oldBucket := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	seedRawResult(t, dbSvc, org, check, models.ResultStatusCreated, oldBucket.Add(1*time.Minute))
	seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, oldBucket.Add(2*time.Minute))

	run := &AggregationJobRun{}
	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	checkUID, _, _, found, err := run.findAggregatableResults(ctx, jctx, org.UID, periodRaw)
	r.NoError(err)
	r.True(found, "a bucket with a measurable row must still be discovered")
	r.Equal(check.UID, checkUID, "discovery must return the check that owns the measurable row")
}

// TestAggregatePeriod_MarkerOnlyBucketIsInert pins spec 2026-07-11-16 §2: a
// forced aggregation over a marker-only bucket inserts nothing, returns
// aggregated=false, and leaves the marker in place (never a degenerate rollup).
func TestAggregatePeriod_MarkerOnlyBucketIsInert(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	oldBucket := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	created := seedRawResult(t, dbSvc, org, check, models.ResultStatusCreated, oldBucket.Add(1*time.Minute))

	run := &AggregationJobRun{}
	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	aggregated, err := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.False(aggregated, "a marker-only bucket must not report work done (the loop must terminate)")

	r.Empty(listHourRows(t, dbSvc, org, check), "no hour row may be produced for a marker-only bucket")

	kept, err := dbSvc.GetResult(ctx, created.UID)
	r.NoError(err, "the lifecycle marker must survive")
	r.Equal(int(models.ResultStatusCreated), *kept.Status)
	r.Equal(periodRaw, kept.PeriodType)
}

// TestAggregatePeriod_TerminatesLoopAfterMixedBucket is the end-to-end
// poison-pill proof: a bucket with measurable rows + preserved markers rolls up
// once, then its marker-only remainder is inert on the next pass — no
// re-aggregation, no unbounded duplicate hour rows.
func TestAggregatePeriod_TerminatesLoopAfterMixedBucket(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	oldBucket := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	created := seedRawResult(t, dbSvc, org, check, models.ResultStatusCreated, oldBucket.Add(1*time.Minute))
	up1 := seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, oldBucket.Add(2*time.Minute))
	up2 := seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, oldBucket.Add(3*time.Minute))

	run := &AggregationJobRun{}
	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	// First pass: the two measurable rows roll up into a single hour row.
	first, err := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.True(first, "a bucket with measurable rows must aggregate")

	hourRows := listHourRows(t, dbSvc, org, check)
	r.Len(hourRows, 1)
	r.NotNil(hourRows[0].TotalChecks)
	r.Equal(2, *hourRows[0].TotalChecks)

	_, err = dbSvc.GetResult(ctx, up1.UID)
	r.Error(err, "measurable rows must be deleted after rollup")
	_, err = dbSvc.GetResult(ctx, up2.UID)
	r.Error(err)

	_, err = dbSvc.GetResult(ctx, created.UID)
	r.NoError(err, "the lifecycle marker must be preserved")

	// Second pass: the leftover marker is all that remains in the bucket. It
	// must NOT be re-discovered/re-aggregated, and no duplicate hour row may
	// appear — this is the poison-pill loop neutralized.
	second, err := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.False(second, "the leftover marker-only bucket must be inert")

	r.Len(listHourRows(t, dbSvc, org, check), 1, "no duplicate hour row may be produced on re-aggregation")
}

// TestUpsertAggregatedResult_Idempotent pins spec 2026-07-11-16 §3: writing the
// same aggregated bucket twice yields exactly one row, for NULL region (the
// former unique-index hole) and for a set region alike.
func TestUpsertAggregatedResult_Idempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	periodStart := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	periodEnd := periodStart.Add(time.Hour)

	build := func(region *string, totalChecks int) *models.Result {
		status := int(models.ResultStatusUp)
		count := totalChecks
		end := periodEnd

		return &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      periodHour,
			PeriodStart:     periodStart,
			PeriodEnd:       &end,
			Region:          region,
			Status:          &status,
			TotalChecks:     &count,
			CreatedAt:       time.Now(),
		}
	}

	// Two writes for the NULL-region bucket → one row (the hole the old index
	// missed). Two writes for the "eu" bucket → one row (a distinct key).
	r.NoError(dbSvc.UpsertAggregatedResult(ctx, build(nil, 5)))
	r.NoError(dbSvc.UpsertAggregatedResult(ctx, build(nil, 9)))

	region := "eu"
	r.NoError(dbSvc.UpsertAggregatedResult(ctx, build(&region, 3)))
	r.NoError(dbSvc.UpsertAggregatedResult(ctx, build(&region, 7)))

	rows := listHourRows(t, dbSvc, org, check)
	r.Len(rows, 2, "one row per bucket key (NULL region + eu), no duplicates")

	for _, row := range rows {
		r.NotNil(row.TotalChecks)
		if row.Region == nil {
			r.Equal(9, *row.TotalChecks, "NULL-region row reflects the last upsert")
		} else {
			r.Equal(7, *row.TotalChecks, "eu row reflects the last upsert")
		}
	}
}

// TestRun_MarkerOnlyReschedulesInOneHour pins that when no real work exists
// (only an inert old marker), Run reschedules the next aggregation an hour out
// — not at delay=0, which was the runaway loop.
func TestRun_MarkerOnlyReschedulesInOneHour(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	oldBucket := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	created := seedRawResult(t, dbSvc, org, check, models.ResultStatusCreated, oldBucket.Add(1*time.Minute))

	jobs := &recordingJobService{}
	jctx := &jobdef.JobContext{
		OrganizationUID: &org.UID,
		DBService:       dbSvc,
		Services:        &services.Registry{Jobs: jobs},
		Logger:          slog.Default(),
	}

	run := &AggregationJobRun{}
	r.NoError(run.Run(ctx, jctx))

	r.Len(jobs.scheduledAt, 1)
	r.WithinDuration(time.Now().Add(1*time.Hour), jobs.scheduledAt[0], 2*time.Minute,
		"no-work runs must reschedule ~1h out, never immediately")

	_, err := dbSvc.GetResult(ctx, created.UID)
	r.NoError(err, "the marker must remain untouched")
	r.Empty(listHourRows(t, dbSvc, org, check), "no hour row may be produced")
}

// TestRetentionFromConfig_DefaultsAndDBParameter pins that an unconfigured
// deployment resolves to the real defaults (24/7/2), never the old 1/1/1; a
// global DB parameter overrides its tier; an invalid (< 1) DB value warns and
// falls through to the default.
func TestRetentionFromConfig_DefaultsAndDBParameter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, _, _ := newAggTestEnv(t)

	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	// The literal default triple is the user-facing contract (spec 2026-07-14-02):
	// raw stays 24h, hourly tightened to 7 days, daily to 2 months.
	r.Equal(24, defaultRetentionRawHours)
	r.Equal(7, defaultRetentionHourDays)
	r.Equal(2, defaultRetentionDayMonths)

	raw, hour, day := retentionFromConfig(ctx, jctx)
	r.Equal(defaultRetentionRawHours, raw, "raw retention defaults to 24h, not 1")
	r.Equal(defaultRetentionHourDays, hour)
	r.Equal(defaultRetentionDayMonths, day)

	// A valid global DB parameter overrides just its tier.
	r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPerfAggRetentionRawHours), 5, false))
	raw, hour, day = retentionFromConfig(ctx, jctx)
	r.Equal(5, raw, "a global DB parameter must override the raw tier")
	r.Equal(defaultRetentionHourDays, hour)
	r.Equal(defaultRetentionDayMonths, day)

	// An invalid (< 1) value must warn and fall through to the default.
	r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPerfAggRetentionRawHours), 0, false))
	raw, _, _ = retentionFromConfig(ctx, jctx)
	r.Equal(defaultRetentionRawHours, raw, "an invalid parameter must fall through to the default")
}

// TestRetentionFromConfig_EnvOverrides pins that an SP_PERFORMANCE_* env var
// wins over the DB parameter. Not parallel: it mutates process env.
func TestRetentionFromConfig_EnvOverrides(t *testing.T) {
	r := require.New(t)
	ctx := t.Context()
	dbSvc, _, _ := newAggTestEnv(t)

	// DB says 5, env says 9 — env must win.
	r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPerfAggRetentionRawHours), 5, false))
	t.Setenv("SP_PERFORMANCE_AGGREGATION_RETENTION_RAW_HOURS", "9")

	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}
	raw, _, _ := retentionFromConfig(ctx, jctx)
	r.Equal(9, raw, "an env override must win over the DB parameter")
}

// TestRun_WorkDoneReschedulesImmediately is the contrast: when a bucket really
// aggregates, Run reschedules immediately (delay=0) to drain the backlog.
func TestRun_WorkDoneReschedulesImmediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	oldBucket := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, oldBucket.Add(1*time.Minute))
	seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, oldBucket.Add(2*time.Minute))

	jobs := &recordingJobService{}
	jctx := &jobdef.JobContext{
		OrganizationUID: &org.UID,
		DBService:       dbSvc,
		Services:        &services.Registry{Jobs: jobs},
		Logger:          slog.Default(),
	}

	run := &AggregationJobRun{}
	r.NoError(run.Run(ctx, jctx))

	r.Len(jobs.scheduledAt, 1)
	r.WithinDuration(time.Now(), jobs.scheduledAt[0], 2*time.Minute,
		"a run that aggregated must reschedule immediately")
	r.Len(listHourRows(t, dbSvc, org, check), 1)
}
