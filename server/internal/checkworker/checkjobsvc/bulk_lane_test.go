package checkjobsvc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// bulkTestDB is the slice of db.Service the bulk-lane suite needs; both
// dialects provide it.
type bulkTestDB interface {
	DB() *bun.DB
	CreateOrganization(ctx context.Context, org *models.Organization) error
	CreateCheck(ctx context.Context, check *models.Check) error
	CreateCheckJob(ctx context.Context, job *models.CheckJob) error
}

type bulkFixture struct {
	t   *testing.T
	db  bulkTestDB
	svc checkjobsvc.Service
	org *models.Organization
}

func newBulkFixture(t *testing.T, dbSvc bulkTestDB) *bulkFixture {
	t.Helper()

	org := models.NewOrganization("bulk-"+uuid.NewString()[:8], "Bulk Org")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

	// Park every job of this fixture when the subtest ends, so the next
	// subtest's region-less claims only see its own jobs.
	t.Cleanup(func() {
		_, _ = dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("scheduled_at = ?", time.Now().Add(48*time.Hour)).
			Set("lease_worker_uid = NULL").
			Set("lease_expires_at = NULL").
			Where("organization_uid = ?", org.UID).
			Exec(context.Background())
	})

	return &bulkFixture{t: t, db: dbSvc, svc: checkjobsvc.NewService(dbSvc.DB()), org: org}
}

func (f *bulkFixture) worker() string {
	f.t.Helper()

	worker := models.NewWorker("bulk-worker-"+uuid.NewString()[:8], "Bulk Worker")
	_, err := f.db.DB().NewInsert().Model(worker).Exec(f.t.Context())
	require.NoError(f.t, err)

	return worker.UID
}

// job creates one job of checkType in lane, due at scheduledAt.
func (f *bulkFixture) job(checkType string, lane uint8, scheduledAt time.Time) *models.CheckJob {
	f.t.Helper()

	check := models.NewCheck(f.org.UID, checkType+"-"+uuid.NewString()[:8], checkType)
	check.Enabled = false // no auto-created job
	require.NoError(f.t, f.db.CreateCheck(f.t.Context(), check))

	job := models.NewCheckJob(f.org.UID, check.UID, timeutils.Duration(24*time.Hour))
	job.Type = checkType
	job.Lane = lane
	job.ScheduledAt = &scheduledAt
	job.EffectiveScheduledAt = &scheduledAt
	require.NoError(f.t, f.db.CreateCheckJob(f.t.Context(), job))

	return job
}

func lanesOf(jobs []*models.CheckJob) map[uint8]int {
	out := map[uint8]int{}
	for _, job := range jobs {
		out[job.Lane]++
	}

	return out
}

// runBulkLaneSuite is the dialect-agnostic bulk lane contract (spec
// 2026-10-03-03).
func runBulkLaneSuite(t *testing.T, dbSvc bulkTestDB) {
	t.Helper()

	t.Run("no claim-ahead for bulk", func(t *testing.T) {
		f := newBulkFixture(t, dbSvc)
		soon := time.Now().Add(time.Second)
		bulk := f.job("crawl", scheduling.LaneBulk, soon)
		fast := f.job("sleep", scheduling.LaneFast, soon)

		claimed, _, err := f.svc.ClaimJobs(t.Context(), f.worker(), nil, 10, 10, 10, 5*time.Minute)
		require.NoError(t, err)

		uids := map[string]bool{}
		for _, job := range claimed {
			uids[job.UID] = true
		}

		require.True(t, uids[fast.UID], "a fast job due in 1s is claimed ahead")
		require.False(t, uids[bulk.UID], "a bulk job due in 1s is not")

		releaseAll(t, f.svc, claimed)
	})

	t.Run("slow then bulk then fast, bulk budget shared with slow", func(t *testing.T) {
		f := newBulkFixture(t, dbSvc)
		past := time.Now().Add(-time.Minute)

		for range 2 {
			f.job("sleep", scheduling.LaneSlow, past)
			f.job("crawl", scheduling.LaneBulk, past)
		}

		for range 3 {
			f.job("sleep", scheduling.LaneFast, past)
		}

		// Capacity 3, slow budget 1, bulk budget 2 (before slow is deducted).
		claimed, _, err := f.svc.ClaimJobs(t.Context(), f.worker(), nil, 3, 1, 2, 5*time.Minute)
		require.NoError(t, err)

		lanes := lanesOf(claimed)
		require.Equal(t, 1, lanes[scheduling.LaneSlow], "slow is served first")
		require.Equal(t, 1, lanes[scheduling.LaneBulk], "bulk gets its budget minus the slow claims")
		require.Equal(t, 1, lanes[scheduling.LaneFast], "fast fills what is left")

		for _, job := range claimed {
			if job.Lane == scheduling.LaneBulk {
				require.WithinDuration(t, time.Now().Add(checkjobsvc.BulkLeaseDuration), *job.LeaseExpiresAt,
					5*time.Second, "a slice leases for one slice, not one period")
			}
		}

		releaseAll(t, f.svc, claimed)
	})

	t.Run("bulk limit respected under concurrent claims", func(t *testing.T) {
		f := newBulkFixture(t, dbSvc)
		past := time.Now().Add(-time.Minute)

		for range 10 {
			f.job("crawl", scheduling.LaneBulk, past)
		}

		var (
			mu      sync.Mutex
			batches [][]*models.CheckJob
			wg      sync.WaitGroup
		)

		for range 4 {
			workerUID := f.worker()

			wg.Go(func() {
				claimed, _, err := f.svc.ClaimJobs(context.Background(), workerUID, nil, 10, 0, 2, 5*time.Minute)
				if err != nil {
					return // SQLite optimistic lock: a lost race claims nothing
				}

				mu.Lock()
				defer mu.Unlock()

				batches = append(batches, claimed)
			})
		}

		wg.Wait()

		seen := map[string]int{}

		for _, claimed := range batches {
			require.LessOrEqual(t, lanesOf(claimed)[scheduling.LaneBulk], 2, "bulk limit per claim")

			for _, job := range claimed {
				seen[job.UID]++
			}

			releaseAll(t, f.svc, claimed)
		}

		for uid, count := range seen {
			require.Equal(t, 1, count, "job %s claimed twice", uid)
		}
	})

	t.Run("agents never claim bulk jobs", func(t *testing.T) {
		f := newBulkFixture(t, dbSvc)
		region := "eu-" + uuid.NewString()[:6]
		past := time.Now().Add(-time.Minute)

		bulk := f.job("crawl", scheduling.LaneBulk, past)
		_, err := f.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("region = ?", region).Where("uid = ?", bulk.UID).Exec(t.Context())
		require.NoError(t, err)

		claimed, _, err := f.svc.ClaimJobsForAgent(t.Context(), f.worker(),
			checkjobsvc.AgentScope{Region: region, System: true}, "", 10, 5*time.Minute)
		require.NoError(t, err)
		require.Empty(t, claimed)
	})

	t.Run("step writes are fenced on the lease and the run", func(t *testing.T) {
		runStepFencingCase(t, newBulkFixture(t, dbSvc))
	})
}

func runStepFencingCase(t *testing.T, f *bulkFixture) {
	t.Helper()

	ctx := t.Context()
	f.job("crawl", scheduling.LaneBulk, time.Now().Add(-time.Minute))

	workerA := f.worker()
	claimed, _, err := f.svc.ClaimJobs(ctx, workerA, nil, 1, 0, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	job := claimed[0]
	started := time.Now().Add(-time.Second).UTC().Truncate(time.Second)
	stateA := "state-a"

	// Slice 1 (worker A) starts the run.
	require.NoError(t, f.svc.SubmitStep(ctx, job.UID, workerA, &checkjobsvc.StepUpdate{
		RunUID: "run-1", RunStartedAt: started, StateFileUID: &stateA, NextAt: time.Now(),
	}))

	// Worker B claims slice 2 and advances the run.
	workerB := f.worker()
	claimed, _, err = f.svc.ClaimJobs(ctx, workerB, nil, 1, 0, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "run-1", *claimed[0].StepRunUID)
	require.Equal(t, 1, claimed[0].StepCount)

	stateB := "state-b"
	run := "run-1"
	require.NoError(t, f.svc.SubmitStep(ctx, job.UID, workerB, &checkjobsvc.StepUpdate{
		ExpectedRunUID: &run, RunUID: run, RunStartedAt: started, StateFileUID: &stateB, NextAt: time.Now(),
	}))

	// A stale worker A, whose lease is long gone, changes nothing.
	stale := "state-stale"
	err = f.svc.SubmitStep(ctx, job.UID, workerA, &checkjobsvc.StepUpdate{
		RunUID: "run-1", RunStartedAt: started, StateFileUID: &stale, NextAt: time.Now(),
	})
	require.ErrorIs(t, err, checkjobsvc.ErrJobClaimedByAnother)

	stored := new(models.CheckJob)
	require.NoError(t, f.db.DB().NewSelect().Model(stored).Where("uid = ?", job.UID).Scan(ctx))
	require.Equal(t, "state-b", *stored.StepStateFileUID)
	require.Equal(t, 2, stored.StepCount)

	// A claim under another run fence cannot end the run either.
	workerC := f.worker()
	claimed, _, err = f.svc.ClaimJobs(ctx, workerC, nil, 1, 0, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	other := "run-other"
	require.ErrorIs(t, f.svc.EndStepRun(ctx, job.UID, workerC, &other, time.Now().Add(time.Hour)),
		checkjobsvc.ErrJobClaimedByAnother)

	next := time.Now().Add(time.Hour)
	require.NoError(t, f.svc.EndStepRun(ctx, job.UID, workerC, &run, next))

	stored = new(models.CheckJob)
	require.NoError(t, f.db.DB().NewSelect().Model(stored).Where("uid = ?", job.UID).Scan(ctx))
	require.Nil(t, stored.StepRunUID)
	require.Nil(t, stored.StepStateFileUID)
	require.Nil(t, stored.StepRunStartedAt)
	require.Zero(t, stored.StepCount)
	require.Nil(t, stored.LeaseWorkerUID)
	require.WithinDuration(t, next, *stored.ScheduledAt, time.Second)
	require.Equal(t, scheduling.LaneBulk, stored.Lane, "the run end keeps the job on the bulk lane")
}

func releaseAll(t *testing.T, svc checkjobsvc.Service, jobs []*models.CheckJob) {
	t.Helper()

	for _, job := range jobs {
		require.NoError(t, svc.ReleaseLease(t.Context(), job.UID, *job.LeaseWorkerUID, time.Now().Add(48*time.Hour)))
	}
}

//nolint:paralleltest // the suite shares one database
func TestBulkLane_SQLite(t *testing.T) {
	dbSvc, _ := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runBulkLaneSuite(t, dbSvc)
}

// A crawl check's auto-created job starts on the bulk lane.
//
//nolint:paralleltest // shares an in-memory database helper
func TestCrawlCheckJobStartsOnTheBulkLane(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := createTestOrg(t, ctx, dbSvc)
	check := models.NewCheck(org.UID, "crawl-site", "crawl")
	check.Config = models.JSONMap{"url": "https://www.acme.com/"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))
	require.Equal(t, scheduling.LaneBulk, job.Lane)
}

//nolint:paralleltest // the suite shares one database
func TestBulkLane_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: 15452, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runBulkLaneSuite(t, dbSvc)
}
