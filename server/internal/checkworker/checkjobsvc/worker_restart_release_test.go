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

// portWorkerRestartRelease is this file's embedded-Postgres port.
const portWorkerRestartRelease = 15686

// restartFixture is one org, two workers and a helper that leases a fresh
// check's job to a worker with a lease that is still valid for an hour.
type restartFixture struct {
	t     *testing.T
	ctx   context.Context //nolint:containedctx // test fixture, scoped to one test
	dbSvc db.Service
	bunDB *bun.DB
	org   *models.Organization
}

func newRestartFixture(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) *restartFixture {
	t.Helper()

	org := models.NewOrganization("rst"+uuid.New().String()[:8], "Restart Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return &restartFixture{t: t, ctx: ctx, dbSvc: dbSvc, bunDB: bunDB, org: org}
}

func (f *restartFixture) newWorker() *models.Worker {
	f.t.Helper()

	worker := models.NewWorker("rw-"+uuid.New().String()[:8], "Restart Worker")
	_, err := f.bunDB.NewInsert().Model(worker).Exec(f.ctx)
	require.NoError(f.t, err)

	return worker
}

// leasedJob creates a check and leases its job to workerUID as a claim at
// claimedAt would: due 10 s before it, lease valid for an hour, one start.
func (f *restartFixture) leasedJob(workerUID string, claimedAt time.Time) *models.CheckJob {
	f.t.Helper()
	r := require.New(f.t)

	check := models.NewCheck(f.org.UID, "rc-"+uuid.New().String()[:8], "http")
	check.Config = models.JSONMap{"url": "https://example.com"}
	r.NoError(f.dbSvc.CreateCheck(f.ctx, check))

	jobs, err := f.dbSvc.ListCheckJobsByCheckUID(f.ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	scheduledAt := claimedAt.Add(-10 * time.Second).UTC().Truncate(time.Millisecond)

	_, err = f.bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", scheduledAt).
		Set("effective_scheduled_at = ?", scheduledAt).
		Set("lease_worker_uid = ?", workerUID).
		Set("lease_expires_at = ?", claimedAt.Add(time.Hour)).
		Set("lease_starts = 1").
		Set("updated_at = ?", claimedAt).
		Where("uid = ?", jobs[0].UID).
		Exec(f.ctx)
	r.NoError(err)

	return loadJob(f.ctx, f.t, f.bunDB, jobs[0].UID)
}

func (f *restartFixture) set(jobUID, assignment string, value any) {
	f.t.Helper()

	_, err := f.bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set(assignment, value).Where("uid = ?", jobUID).Exec(f.ctx)
	require.NoError(f.t, err)
}

// exerciseReleaseWorkerLeases is the restart contract of spec 2026-11-14-03.
func exerciseReleaseWorkerLeases(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(bunDB)
	f := newRestartFixture(ctx, t, dbSvc, bunDB)

	workerA := f.newWorker()
	workerB := f.newWorker()
	workerC := f.newWorker()

	start := time.Now()
	before := start.Add(-time.Minute)

	plain := f.leasedJob(workerA.UID, before)
	captured := f.leasedJob(workerA.UID, before)
	runRequested := f.leasedJob(workerA.UID, before)
	other := f.leasedJob(workerB.UID, before)
	// A's slug is shared by a sibling that claimed after this process started.
	sibling := f.leasedJob(workerA.UID, start.Add(time.Second))

	captureAt := before.Add(-time.Second).UTC().Truncate(time.Millisecond)
	f.set(captured.UID, "capture_claimed_at = ?", captureAt)

	runAt := before.Add(-2 * time.Second).UTC().Truncate(time.Millisecond)
	f.set(runRequested.UID, "run_requested_at = ?", runAt)

	released, err := svc.ReleaseWorkerLeases(ctx, workerA.UID, start)
	r.NoError(err)
	r.Equal(3, released, "every lease A took before this process started is freed")

	for _, job := range []*models.CheckJob{plain, captured, runRequested} {
		got := loadJob(ctx, t, bunDB, job.UID)
		r.Nil(got.LeaseWorkerUID)
		r.Nil(got.LeaseExpiresAt)
		r.Equal(1, got.LeaseStarts, "the crash counter is kept: the lease ended without a release")
		r.NotNil(got.ScheduledAt)
		r.WithinDuration(*job.ScheduledAt, *got.ScheduledAt, time.Millisecond, "scheduled_at is left alone")
		r.WithinDuration(*job.EffectiveScheduledAt, *got.EffectiveScheduledAt, time.Millisecond)

		// Claimable right away, without waiting for the old lease to expire.
		claimed, claimErr := svc.ClaimJobsForCheck(ctx, workerC.UID, job.Region, job.CheckUID)
		r.NoError(claimErr)
		r.Len(claimed, 1, "a freed job is claimable at once")
		r.Equal(job.UID, claimed[0].UID)
	}

	gotCaptured := loadJob(ctx, t, bunDB, captured.UID)
	// The claim above consumed the request again; what matters is it carried it.
	r.NotNil(gotCaptured.CaptureClaimedAt, "the freed capture request went back to pending, then to the new claim")
	r.WithinDuration(captureAt, *gotCaptured.CaptureClaimedAt, time.Millisecond)

	// Positive control: B's lease is untouched.
	gotOther := loadJob(ctx, t, bunDB, other.UID)
	r.NotNil(gotOther.LeaseWorkerUID)
	r.Equal(workerB.UID, *gotOther.LeaseWorkerUID)
	r.WithinDuration(*other.LeaseExpiresAt, *gotOther.LeaseExpiresAt, time.Millisecond)
	r.Equal(1, gotOther.LeaseStarts)

	// A lease claimed after this process started is a live sibling's.
	gotSibling := loadJob(ctx, t, bunDB, sibling.UID)
	r.NotNil(gotSibling.LeaseWorkerUID)
	r.Equal(workerA.UID, *gotSibling.LeaseWorkerUID)

	released, err = svc.ReleaseWorkerLeases(ctx, workerB.UID, before.Add(-time.Hour))
	r.NoError(err)
	r.Zero(released, "nothing claimed before the cutoff")

	nobody := f.newWorker()
	released, err = svc.ReleaseWorkerLeases(ctx, nobody.UID, time.Now())
	r.NoError(err)
	r.Zero(released, "no leases held: 0, no error")
}

// exerciseReleaseFieldsBeforeReclaim checks the released row itself, before
// any new claim touches it: the capture request is pending again and a parked
// run request is kept.
func exerciseReleaseFieldsBeforeReclaim(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(bunDB)
	f := newRestartFixture(ctx, t, dbSvc, bunDB)
	worker := f.newWorker()

	before := time.Now().Add(-time.Minute)
	job := f.leasedJob(worker.UID, before)

	captureAt := before.Add(-time.Second).UTC().Truncate(time.Millisecond)
	runAt := before.Add(-2 * time.Second).UTC().Truncate(time.Millisecond)
	f.set(job.UID, "capture_claimed_at = ?", captureAt)
	f.set(job.UID, "run_requested_at = ?", runAt)

	released, err := svc.ReleaseWorkerLeases(ctx, worker.UID, time.Now())
	r.NoError(err)
	r.Equal(1, released)

	got := loadJob(ctx, t, bunDB, job.UID)
	r.Nil(got.CaptureClaimedAt)
	r.NotNil(got.CaptureRequestedAt, "a pending Capture now request is not lost")
	r.WithinDuration(captureAt, *got.CaptureRequestedAt, time.Millisecond)
	r.NotNil(got.RunRequestedAt, "no probe ran: the run request stays pending")
	r.WithinDuration(runAt, *got.RunRequestedAt, time.Millisecond)
}

// exerciseReleaseOnNewBoot: only a CHANGED boot ID frees the leases.
func exerciseReleaseOnNewBoot(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(bunDB)
	f := newRestartFixture(ctx, t, dbSvc, bunDB)
	worker := f.newWorker()

	job := f.leasedJob(worker.UID, time.Now().Add(-time.Minute))

	bootOf := func() *string {
		row := new(models.Worker)
		r.NoError(bunDB.NewSelect().Model(row).Where("uid = ?", worker.UID).Scan(ctx))

		return row.BootID
	}

	stillLeased := func(msg string) {
		got := loadJob(ctx, t, bunDB, job.UID)
		r.NotNil(got.LeaseWorkerUID, msg)
	}

	released, err := svc.ReleaseLeasesOnNewBoot(ctx, worker.UID, "")
	r.NoError(err)
	r.Zero(released)
	stillLeased("an agent that sends no boot ID keeps today's behavior")
	r.Nil(bootOf())

	released, err = svc.ReleaseLeasesOnNewBoot(ctx, worker.UID, "boot-1")
	r.NoError(err)
	r.Zero(released)
	stillLeased("a first boot ID cannot tell a restart from a reconnect")
	r.NotNil(bootOf())
	r.Equal("boot-1", *bootOf())

	released, err = svc.ReleaseLeasesOnNewBoot(ctx, worker.UID, "boot-1")
	r.NoError(err)
	r.Zero(released)
	stillLeased("a reconnect from the same process keeps its leases")

	released, err = svc.ReleaseLeasesOnNewBoot(ctx, worker.UID, "boot-2")
	r.NoError(err)
	r.Equal(1, released, "a new process frees its predecessor's leases")
	r.Equal("boot-2", *bootOf())

	got := loadJob(ctx, t, bunDB, job.UID)
	r.Nil(got.LeaseWorkerUID)
	r.Nil(got.LeaseExpiresAt)
}

func runRestartRelease(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	exerciseReleaseWorkerLeases(ctx, t, dbSvc, bunDB)
	exerciseReleaseFieldsBeforeReclaim(ctx, t, dbSvc, bunDB)
	exerciseReleaseOnNewBoot(ctx, t, dbSvc, bunDB)
}

// TestReleaseWorkerLeases runs the restart contract on SQLite.
func TestReleaseWorkerLeases(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runRestartRelease(ctx, t, dbSvc, dbSvc.DB())
}

// TestReleaseWorkerLeases_Postgres runs the same contract on Postgres.
func TestReleaseWorkerLeases_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portWorkerRestartRelease, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runRestartRelease(ctx, t, dbSvc, dbSvc.DB())
}
