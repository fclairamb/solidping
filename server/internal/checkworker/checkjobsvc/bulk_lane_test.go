package checkjobsvc_test

import (
	"context"
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

// runBulkLaneSuite pins that no check_jobs claim hands out a bulk-lane job
// (a website crawl) any more: a crawl run is a worker task (spec
// 2026-10-22-03). A due fast job claimed by the same calls is the positive
// control.
func runBulkLaneSuite(t *testing.T, dbSvc bulkTestDB) {
	t.Helper()

	t.Run("cloud claims skip bulk jobs", func(t *testing.T) {
		f := newBulkFixture(t, dbSvc)
		due := time.Now().Add(-time.Minute)
		bulk := f.job("crawl", scheduling.LaneBulk, due)
		fast := f.job("sleep", scheduling.LaneFast, due)

		claimed, _, err := f.svc.ClaimJobs(t.Context(), f.worker(), nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)

		uids := map[string]bool{}
		for _, job := range claimed {
			uids[job.UID] = true
		}

		require.True(t, uids[fast.UID], "positive control: the due fast job is claimed")
		require.False(t, uids[bulk.UID], "a due bulk job is never claimed by a runner")
		require.Zero(t, lanesOf(claimed)[scheduling.LaneBulk])

		releaseAll(t, f.svc, claimed)

		express, err := f.svc.ClaimJobsForCheck(t.Context(), f.worker(), nil, bulk.CheckUID)
		require.NoError(t, err)
		require.Empty(t, express, "the express path never claims a crawl")
	})

	t.Run("a bulk job never feeds the next-eligible hint", func(t *testing.T) {
		f := newBulkFixture(t, dbSvc)
		f.job("crawl", scheduling.LaneBulk, time.Now().Add(30*time.Second))

		_, nextIn, err := f.svc.ClaimJobs(t.Context(), f.worker(), nil, 10, 10, 0)
		require.NoError(t, err)
		require.True(t, nextIn == 0 || nextIn > 30*time.Second,
			"the only upcoming job is a crawl, so no hint points at it (got %s)", nextIn)
	})
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
