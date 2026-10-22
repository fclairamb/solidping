package checkjobsvc_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portRunRequest is this file's embedded-Postgres port.
const portRunRequest = 15619

// exerciseRequestCheckRun pins RequestCheckRun (specs 2026-10-04-01 and
// 2026-10-12-05) on both engines: an idle job has both schedule columns moved
// to requestedAt and the capture flag left NULL; a leased job keeps its
// schedule and parks the request in run_requested_at, which the release turns
// into a due time; a release with nothing parked schedules the next period
// (the control); a missing job is sql.ErrNoRows.
func exerciseRequestCheckRun(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(bunDB)

	org := models.NewOrganization("rr"+uuid.New().String()[:8], "Run Request Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("rw-"+uuid.New().String()[:8], "Run Request Worker")
	_, err := bunDB.NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)

	check := models.NewCheck(org.UID, "rr-"+uuid.New().String()[:8], "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	job := jobs[0]

	far := time.Now().Add(time.Hour).UTC()
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", far).Set("effective_scheduled_at = ?", far).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)

	requestedAt := time.Now().UTC().Truncate(time.Millisecond)
	r.NoError(dbSvc.RequestCheckRun(ctx, job.UID, requestedAt))

	stored := loadJob(ctx, t, bunDB, job.UID)
	r.WithinDuration(requestedAt, *stored.ScheduledAt, time.Millisecond)
	r.WithinDuration(requestedAt, *stored.EffectiveScheduledAt, time.Millisecond)
	r.Nil(stored.CaptureRequestedAt, "run-now never sets the capture flag")
	r.Nil(stored.RunRequestedAt, "an idle job is made due, nothing is parked")

	// A worker claims it: the run is in flight.
	claimed, err := svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1)

	// Leased: the schedule is left alone and the request is parked.
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", far).Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)

	parkedAt := time.Now().UTC().Truncate(time.Millisecond)
	r.NoError(dbSvc.RequestCheckRun(ctx, job.UID, parkedAt))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.WithinDuration(far, *stored.ScheduledAt, time.Millisecond, "the lease's schedule is untouched")
	r.NotNil(stored.RunRequestedAt, "the request is parked for the release")
	r.WithinDuration(parkedAt, *stored.RunRequestedAt, time.Millisecond)

	// A second request keeps the earliest one.
	r.NoError(dbSvc.RequestCheckRun(ctx, job.UID, parkedAt.Add(time.Second)))
	r.WithinDuration(parkedAt, *loadJob(ctx, t, bunDB, job.UID).RunRequestedAt, time.Millisecond)

	// The release makes the job due at the request and clears it, with no
	// ErrJobClaimedByAnother.
	next := time.Now().Add(30 * time.Minute).UTC()
	r.NoError(svc.ReleaseLeaseWithSchedulingState(ctx, job.UID, worker.UID, next, 10, 0, next, 0))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.RunRequestedAt, "the release spends the request")
	r.WithinDuration(parkedAt, *stored.ScheduledAt, time.Millisecond, "due at the request time")
	r.WithinDuration(parkedAt, *stored.EffectiveScheduledAt, time.Millisecond)

	claimed, err = svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1, "the second run is claimable at once")

	// Control: with nothing parked, the release schedules the next period.
	r.NoError(svc.ReleaseLease(ctx, job.UID, worker.UID, next))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.RunRequestedAt)
	r.WithinDuration(next, *stored.ScheduledAt, time.Second)
	r.WithinDuration(next, *stored.EffectiveScheduledAt, time.Second)

	r.ErrorIs(dbSvc.RequestCheckRun(ctx, uuid.New().String(), time.Now().UTC()), sql.ErrNoRows)
}

// exerciseRunRequestReleaseVariants pins that every other release honors a
// parked request (spec 2026-10-12-05): the rate-limit deferral and the end of
// a multi-step run make the job due at it; a multi-step slice keeps it
// pending, since the run goes on; a fresh claim answers a leftover one.
func exerciseRunRequestReleaseVariants(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(bunDB)

	org := models.NewOrganization("rv"+uuid.New().String()[:8], "Run Request Variants")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("rv-"+uuid.New().String()[:8], "Run Request Worker")
	_, err := bunDB.NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)

	check := models.NewCheck(org.UID, "rv-"+uuid.New().String()[:8], "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	job := jobs[0]
	next := time.Now().Add(30 * time.Minute).UTC()

	lease := func() {
		t.Helper()

		due := time.Now().Add(-time.Second).UTC()
		_, updateErr := bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
			Set("scheduled_at = ?", due).Set("effective_scheduled_at = ?", due).
			Where("uid = ?", job.UID).Exec(ctx)
		r.NoError(updateErr)

		claimed, claimErr := svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
		r.NoError(claimErr)
		r.Len(claimed, 1)
	}

	// Rate-limit deferral.
	lease()

	parkedAt := time.Now().UTC().Truncate(time.Millisecond)
	r.NoError(dbSvc.RequestCheckRun(ctx, job.UID, parkedAt))
	r.NoError(svc.DeferLeaseRateLimited(ctx, job.UID, worker.UID, next))

	stored := loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.RunRequestedAt)
	r.WithinDuration(parkedAt, *stored.ScheduledAt, time.Millisecond, "a deferral honors the request")

	// A leftover from a lease that ended without a release is answered by the
	// next fresh claim.
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("run_requested_at = ?", time.Now().Add(-time.Hour).UTC()).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)
	lease()
	r.Nil(loadJob(ctx, t, bunDB, job.UID).RunRequestedAt, "a fresh claim answers a leftover request")

	// Another worker's release is refused as before (the control) ...
	r.ErrorIs(svc.ReleaseLease(ctx, job.UID, uuid.New().String(), next), checkjobsvc.ErrJobClaimedByAnother)

	// ... while a job deleted under the lease is reported gone, not claimed.
	r.NoError(dbSvc.DeleteCheckJob(ctx, job.UID))
	r.ErrorIs(svc.ReleaseLease(ctx, job.UID, worker.UID, next), checkjobsvc.ErrJobGone)
}

// TestRequestCheckRun runs the contract on SQLite.
func TestRequestCheckRun(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	exerciseRequestCheckRun(ctx, t, dbSvc, dbSvc.DB())
	exerciseRunRequestReleaseVariants(ctx, t, dbSvc, dbSvc.DB())
}

// TestRequestCheckRun_Postgres runs the same contract on Postgres.
func TestRequestCheckRun_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portRunRequest, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	exerciseRequestCheckRun(ctx, t, dbSvc, dbSvc.DB())
	exerciseRunRequestReleaseVariants(ctx, t, dbSvc, dbSvc.DB())
}
