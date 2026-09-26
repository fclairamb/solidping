package checkjobsvc_test

import (
	"context"
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

// portCaptureRequest is this file's embedded-Postgres port, distinct from every
// other *_postgres test in the repo.
const portCaptureRequest = 15610

// loadJob reads a job row back as stored.
func loadJob(ctx context.Context, t *testing.T, bunDB *bun.DB, uid string) *models.CheckJob {
	t.Helper()

	job := new(models.CheckJob)
	require.NoError(t, bunDB.NewSelect().Model(job).Where("uid = ?", uid).Scan(ctx))

	return job
}

// exerciseCaptureRequest is the "Capture now" scheduling contract (spec
// 2026-09-25-34), run on both engines:
//
//  1. RequestCheckCapture flags ONE job and makes it due at once, even when
//     its next tick was far away.
//  2. The claim that picks it up returns the flag (that copy is what forces
//     the capture) and consumes it in the row.
//  3. A request that lands WHILE the job is leased survives the release and
//     keeps the job due, instead of being pushed to the next tick; the release
//     of an unflagged job schedules normally (the control).
func exerciseCaptureRequest(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(bunDB)

	org := models.NewOrganization("cap"+uuid.New().String()[:8], "Capture Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("cw-"+uuid.New().String()[:8], "Capture Worker")
	_, err := bunDB.NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)

	check := models.NewCheck(org.UID, "cc-"+uuid.New().String()[:8], "browser")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	job := jobs[0]

	// Its next tick is an hour away: nothing is due.
	farAway := time.Now().Add(time.Hour).UTC()
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", farAway).Set("effective_scheduled_at = ?", farAway).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)

	claimed, err := svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Empty(claimed, "control: before the request nothing is due")

	requestedAt := time.Now().UTC().Truncate(time.Millisecond)
	r.NoError(dbSvc.RequestCheckCapture(ctx, job.UID, requestedAt))

	stored := loadJob(ctx, t, bunDB, job.UID)
	r.NotNil(stored.CaptureRequestedAt)

	claimed, err = svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1, "the request makes the job due at once")
	r.NotNil(claimed[0].CaptureRequestedAt, "the claimed copy carries the request")

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.CaptureRequestedAt, "the claim consumed the request in the row")

	// A second request lands while the job is leased: the release must keep
	// the job due rather than push it to the next tick.
	r.NoError(dbSvc.RequestCheckCapture(ctx, job.UID, time.Now().UTC()))

	next := time.Now().Add(30 * time.Minute).UTC()
	r.NoError(svc.ReleaseLeaseWithSchedulingState(ctx, job.UID, worker.UID, next, 10, 0, next, 0))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.NotNil(stored.CaptureRequestedAt, "a request that arrived mid-lease survives the release")
	r.True(stored.ScheduledAt.Before(time.Now().Add(time.Second)),
		"…and keeps the job due now, not at %s (got %s)", next, stored.ScheduledAt)

	claimed, err = svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1)
	r.NotNil(claimed[0].CaptureRequestedAt)

	// Control: releasing an unflagged job schedules it normally.
	r.NoError(svc.ReleaseLease(ctx, job.UID, worker.UID, next))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.CaptureRequestedAt)
	r.WithinDuration(next, *stored.ScheduledAt, time.Second)
	r.WithinDuration(next, *stored.EffectiveScheduledAt, time.Second)
}

// TestCaptureRequestScheduling runs the contract on SQLite.
func TestCaptureRequestScheduling(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	exerciseCaptureRequest(ctx, t, dbSvc, dbSvc.DB())
}

// TestCaptureRequestScheduling_Postgres runs the same contract on Postgres,
// where the release's CASE has to type-check against a timestamptz column.
func TestCaptureRequestScheduling_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portCaptureRequest, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	exerciseCaptureRequest(ctx, t, dbSvc, dbSvc.DB())
}
