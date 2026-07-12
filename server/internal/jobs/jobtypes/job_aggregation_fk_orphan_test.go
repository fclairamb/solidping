package jobtypes

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// portAggFKOrphanRunPG is distinct from every embedded-Postgres port used by the
// _postgres_test.go files in internal/db/postgres (…15451, 15457…) so this
// cross-package test never collides when `make test` runs packages in parallel.
const portAggFKOrphanRunPG = 15452

// errForcedStage is returned by errOnListResults to make aggregatePeriod fail
// deterministically, standing in for the FK-orphan INSERT that used to halt the
// org's aggregation (spec 2026-07-12-01 §1).
var errForcedStage = errors.New("forced stage failure")

// errOnListResults wraps a real db.Service but forces ListResults — the first
// thing aggregatePeriod calls (via findAggregatableResults) — to error, so Run
// exercises the stage-error path without needing a genuinely poisoned bucket.
type errOnListResults struct {
	db.Service

	err error
}

func (e *errOnListResults) ListResults(
	context.Context, *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	return nil, e.err
}

// hardDeleteCheckNoCascade deletes a check row on a connection with foreign keys
// switched off, so the results→checks ON DELETE CASCADE is suppressed and the
// check's results become FK-orphans — exactly how the production orphans were
// created (a hard delete on a connection that lost FK enforcement).
func hardDeleteCheckNoCascade(t *testing.T, dbSvc *sqlite.Service, checkUID string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	// Pin a single connection so the PRAGMA and the DELETE run together, and
	// release it before the caller runs aggregation (the pool holds one conn).
	conn, err := dbSvc.DB().Conn(ctx)
	r.NoError(err)
	defer func() { _ = conn.Close() }()

	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	r.NoError(err)
	_, err = conn.ExecContext(ctx, "DELETE FROM checks WHERE uid = ?", checkUID)
	r.NoError(err)
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)
}

// seedOrphanRawResult inserts one measurable raw result whose owning check is
// then hard-deleted without cascade, leaving the result orphaned at period_start
// == when. Returns the orphaned result UID.
func seedOrphanRawResult(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, when time.Time,
) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := models.NewCheck(org.UID, "orphan-"+uuid.NewString()[:8], "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Drop the 'created' marker CreateCheck auto-seeds so the only row we leave
	// behind for this check is the measurable orphan below.
	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           100,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	orphan := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.1)
	orphan.PeriodStart = when
	r.NoError(dbSvc.CreateResult(ctx, orphan))

	hardDeleteCheckNoCascade(t, dbSvc, check.UID)

	return orphan.UID
}

// TestListResults_RequireCheckExists_ExcludesOrphans pins spec 2026-07-12-01 §2
// at the DB layer: the RequireCheckExists filter drops rows whose check has been
// hard-deleted, and leaves rows of live checks alone.
func TestListResults_RequireCheckExists_ExcludesOrphans(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	old := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	live := seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, old.Add(2*time.Minute))
	orphanUID := seedOrphanRawResult(t, dbSvc, org, old.Add(5*time.Minute))

	// Without the guard, both rows (live + orphan) are visible.
	all, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		PeriodTypes:     []string{periodRaw},
		Limit:           100,
	})
	r.NoError(err)
	uids := resultUIDSet(all.Results)
	r.Contains(uids, live.UID)
	r.Contains(uids, orphanUID, "control: the orphan is visible without the guard")

	// With the guard, only the live check's row survives.
	guarded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID:    org.UID,
		PeriodTypes:        []string{periodRaw},
		RequireCheckExists: true,
		Limit:              100,
	})
	r.NoError(err)
	guardedUIDs := resultUIDSet(guarded.Results)
	r.Contains(guardedUIDs, live.UID, "a live check's rows must stay visible")
	r.NotContains(guardedUIDs, orphanUID, "an orphaned row must be excluded")
}

func resultUIDSet(results []*models.Result) map[string]bool {
	set := make(map[string]bool, len(results))
	for _, row := range results {
		set[row.UID] = true
	}

	return set
}

// TestFindAggregatableResults_SkipsOrphanBucket pins spec 2026-07-12-01 §2: a
// bucket whose only old row is an FK-orphan is never discovered as aggregatable
// — the deterministic poison pill is starved.
func TestFindAggregatableResults_SkipsOrphanBucket(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, _ := newAggTestEnv(t)

	old := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	seedOrphanRawResult(t, dbSvc, org, old.Add(5*time.Minute))

	run := &AggregationJobRun{}
	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	checkUID, _, _, found, err := run.findAggregatableResults(ctx, jctx, org.UID, periodRaw)
	r.NoError(err)
	r.False(found, "an orphan-only bucket must never be selected by work discovery")
	r.Empty(checkUID)
}

// TestAggregatePeriod_OrphanBucketIsInert pins that forcing an aggregation over
// an orphan-only backlog rolls nothing up, returns aggregated=false with no
// error, and leaves the orphan untouched (never a failed FK INSERT).
func TestAggregatePeriod_OrphanBucketIsInert(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, _ := newAggTestEnv(t)

	old := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	orphanUID := seedOrphanRawResult(t, dbSvc, org, old.Add(5*time.Minute))

	run := &AggregationJobRun{}
	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}

	aggregated, err := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(err, "an orphan bucket must not surface an error from aggregatePeriod")
	r.False(aggregated)

	kept, err := dbSvc.GetResult(ctx, orphanUID)
	r.NoError(err, "the orphan raw row must survive (it is inert, not poison)")
	r.Equal(periodRaw, kept.PeriodType)
}

// TestRun_OrphanDoesNotHaltAggregation is the flagship end-to-end proof: with a
// newer orphan and an older live measurable row both past the retention
// boundary, discovery would (ordered period_start DESC) pick the orphan first
// and fail the rollup INSERT — the original halt. With §2 the orphan is skipped,
// the live row rolls up, Run returns no error and reschedules immediately.
func TestRun_OrphanDoesNotHaltAggregation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, check := newAggTestEnv(t)

	old := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	// Two live measurable rows for the real check (older) ...
	seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, old.Add(2*time.Minute))
	seedRawResult(t, dbSvc, org, check, models.ResultStatusUp, old.Add(3*time.Minute))
	// ... and a newer orphan that the DESC ordering would otherwise pick first.
	orphanUID := seedOrphanRawResult(t, dbSvc, org, old.Add(10*time.Minute))

	jobs := &recordingJobService{}
	jctx := &jobdef.JobContext{
		OrganizationUID: &org.UID,
		DBService:       dbSvc,
		Services:        &services.Registry{Jobs: jobs},
		Logger:          slog.Default(),
	}

	run := &AggregationJobRun{}
	r.NoError(run.Run(ctx, jctx), "the orphan must not halt aggregation")

	// The live check rolled up into exactly one hour row.
	hourRows := listHourRows(t, dbSvc, org, check)
	r.Len(hourRows, 1)
	r.NotNil(hourRows[0].TotalChecks)
	r.Equal(2, *hourRows[0].TotalChecks)

	// The orphan is still present and inert.
	_, err := dbSvc.GetResult(ctx, orphanUID)
	r.NoError(err, "the orphan raw row must remain, never consumed by the rollup")

	// Work was done, so the follow-up is scheduled immediately (not halted).
	r.Len(jobs.scheduledAt, 1)
	r.WithinDuration(time.Now(), jobs.scheduledAt[0], 2*time.Minute,
		"a run that aggregated must reschedule immediately, proving aggregation is alive")
}

// TestRun_OrphanOnlyBacklogReschedules pins that when the entire old backlog is
// orphans (nothing aggregatable), Run still completes cleanly and schedules the
// next run ~1h out — the org's aggregation is never left dead.
func TestRun_OrphanOnlyBacklogReschedules(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, _ := newAggTestEnv(t)

	old := time.Now().UTC().Add(poisonPillOldOffset).Truncate(time.Hour)
	seedOrphanRawResult(t, dbSvc, org, old.Add(5*time.Minute))

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
		"an orphan-only backlog must reschedule ~1h out, never leave aggregation dead")
}

// TestRun_StageErrorStillSchedulesFollowUp pins spec 2026-07-12-01 §1: when an
// aggregation stage errors persistently, Run still schedules the +1h follow-up
// job BEFORE surfacing the (retryable) error — a poisoned bucket degrades to
// "this bucket is stuck", never "aggregation for the org is dead".
func TestRun_StageErrorStillSchedulesFollowUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc, org, _ := newAggTestEnv(t)

	jobs := &recordingJobService{}
	jctx := &jobdef.JobContext{
		OrganizationUID: &org.UID,
		DBService:       &errOnListResults{Service: dbSvc, err: errForcedStage},
		Services:        &services.Registry{Jobs: jobs},
		Logger:          slog.Default(),
	}

	run := &AggregationJobRun{}
	err := run.Run(ctx, jctx)

	r.Error(err, "the stage error must still be surfaced so the job fails/retries visibly")
	r.ErrorIs(err, errForcedStage)

	r.Len(jobs.scheduledAt, 1, "the follow-up job must be scheduled despite the stage error")
	r.WithinDuration(time.Now().Add(1*time.Hour), jobs.scheduledAt[0], 2*time.Minute,
		"a stage error reschedules ~1h out, never at delay=0")
}

// TestRun_StageErrorStillSchedulesFollowUp_Postgres is the Postgres parity case
// for spec 2026-07-12-01 §1/§5. Run's "always schedule the follow-up" control
// flow is backend-independent: the stage error is forced at the ListResults
// boundary via errOnListResults, so the wrapped backend's query path is never
// reached. This variant proves the behavior holds with a real *postgres.Service
// as the DBService — the +1h follow-up is still scheduled AND the stage error is
// still surfaced (errors.Is). Gated behind the same embedded-Postgres
// testcontainer skip as the db/postgres parity tests.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestRun_StageErrorStillSchedulesFollowUp_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	ctx := t.Context()

	pgSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portAggFKOrphanRunPG, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pgSvc.Close() })

	if initErr := pgSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	orgUID := uuid.Must(uuid.NewV7()).String()
	jobs := &recordingJobService{}
	jctx := &jobdef.JobContext{
		OrganizationUID: &orgUID,
		DBService:       &errOnListResults{Service: pgSvc, err: errForcedStage},
		Services:        &services.Registry{Jobs: jobs},
		Logger:          slog.Default(),
	}

	run := &AggregationJobRun{}
	err = run.Run(ctx, jctx)

	r.Error(err, "the stage error must still be surfaced so the job fails/retries visibly")
	r.ErrorIs(err, errForcedStage)

	r.Len(jobs.scheduledAt, 1, "the follow-up job must be scheduled despite the stage error")
	r.WithinDuration(time.Now().Add(1*time.Hour), jobs.scheduledAt[0], 2*time.Minute,
		"a stage error reschedules ~1h out, never at delay=0")
}
