package checkjobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portCaptureRequest is this file's embedded-Postgres port, distinct from every
// other *_postgres test in the repo.
const portCaptureRequest = 15610

// portStaleClaim is the stale-claim test's embedded-Postgres port.
const portStaleClaim = 15616

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
	r.NotNil(claimed[0].CaptureClaimedAt, "…and knows its lease carries it")

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.CaptureRequestedAt, "the claim consumed the request in the row")
	r.NotNil(stored.CaptureClaimedAt,
		"the row records that THIS lease carries a request (what the submission trusts)")

	// A second request lands while the job is leased: the release must keep
	// the job due rather than push it to the next tick.
	r.NoError(dbSvc.RequestCheckCapture(ctx, job.UID, time.Now().UTC()))

	next := time.Now().Add(30 * time.Minute).UTC()
	r.NoError(svc.ReleaseLeaseWithSchedulingState(ctx, job.UID, worker.UID, next, 10, 0, next, 0))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.CaptureClaimedAt, "the release spends the lease's request")
	r.NotNil(stored.CaptureRequestedAt, "a request that arrived mid-lease survives the release")
	r.True(stored.ScheduledAt.Before(time.Now().Add(time.Second)),
		"…and keeps the job due now, not at %s (got %s)", next, stored.ScheduledAt)

	claimed, err = svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1)
	r.NotNil(claimed[0].CaptureRequestedAt)

	// A rate-limit deferral means the probe never ran: the lease's request
	// goes back to pending rather than being lost.
	r.NoError(svc.DeferLeaseRateLimited(ctx, job.UID, worker.UID, next))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.NotNil(stored.CaptureRequestedAt, "a deferred run's request is pending again")
	r.Nil(stored.CaptureClaimedAt)

	r.WithinDuration(next, *stored.ScheduledAt, time.Second,
		"a deferral is not retried at once (the org is over its rate): it rides the next tick")

	// Pull it back to now and claim it again: the pending request is carried.
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", time.Now().Add(-time.Second)).
		Set("effective_scheduled_at = ?", time.Now().Add(-time.Second)).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)

	claimed, err = svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1)
	r.NotNil(claimed[0].CaptureRequestedAt)

	// Control: once no request is pending, the release schedules normally.
	r.NoError(svc.ReleaseLease(ctx, job.UID, worker.UID, next))

	stored = loadJob(ctx, t, bunDB, job.UID)
	r.Nil(stored.CaptureRequestedAt)
	r.WithinDuration(next, *stored.ScheduledAt, time.Second)
	r.WithinDuration(next, *stored.EffectiveScheduledAt, time.Second)
}

// exerciseStaleClaimCleared pins the stale-lease rule: a capture_claimed_at
// left behind by a lease that ended without a release (crash, expiry) is
// cleared by the next claim that carries no request, so that run's onDemand
// marker is not honored.
func exerciseStaleClaimCleared(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(bunDB)

	org := models.NewOrganization("st"+uuid.New().String()[:8], "Stale Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("sw-"+uuid.New().String()[:8], "Stale Worker")
	_, err := bunDB.NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)

	check := models.NewCheck(org.UID, "sc-"+uuid.New().String()[:8], "browser")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	job := jobs[0]
	due := time.Now().Add(-time.Second).UTC()

	// A leftover from a lease that was never released, and no pending request.
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("capture_claimed_at = ?", time.Now().Add(-time.Hour).UTC()).
		Set("capture_requested_at = NULL").
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)
	r.NotNil(loadJob(ctx, t, bunDB, job.UID).CaptureClaimedAt, "control: the leftover is there")

	claimed, err := svc.ClaimJobsForCheck(ctx, worker.UID, job.Region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1)
	r.Nil(claimed[0].CaptureRequestedAt)
	r.Nil(claimed[0].CaptureClaimedAt, "the claimed copy carries no request")
	r.Nil(loadJob(ctx, t, bunDB, job.UID).CaptureClaimedAt, "the claim cleared the leftover in the row")

	// So an agent's onDemand marker on this run is dropped, whichever copy of
	// the job the submission path reads.
	for _, copyOfJob := range []*models.CheckJob{claimed[0], loadJob(ctx, t, bunDB, job.UID)} {
		diagnostics := &checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{OnDemand: true}}
		copyOfJob.HonorOnDemand(diagnostics)
		r.False(diagnostics.Screenshot.OnDemand)
	}
}

// TestStaleClaimClearedByNextClaim runs it on SQLite.
func TestStaleClaimClearedByNextClaim(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	exerciseStaleClaimCleared(ctx, t, dbSvc, dbSvc.DB())
}

// TestStaleClaimClearedByNextClaim_Postgres runs it on Postgres.
func TestStaleClaimClearedByNextClaim_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portStaleClaim, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	exerciseStaleClaimCleared(ctx, t, dbSvc, dbSvc.DB())
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
