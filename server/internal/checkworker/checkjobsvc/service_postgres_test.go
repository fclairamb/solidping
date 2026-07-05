package checkjobsvc_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// TestClaimJobsBoundedClaimAheadWindow_Postgres exercises the D3 per-job
// claim-ahead clamp (spec 2026-07-05-08) against a real embedded PostgreSQL,
// proving the SQLite-tested behavior (TestClaimJobsBoundedClaimAheadWindow in
// service_test.go) holds identically on both backends — the SQL gate and the
// Go-side per-row post-filter are dialect-agnostic (no interval arithmetic in
// SQL), so this asserts the two dialects agree rather than testing new
// behavior.
//
// Self-skips under `-short` (the default `make test` mode) and on any
// embedded-startup error, mirroring
// internal/scheduling/costdist/costdist_postgres_test.go. Uses port 15437 —
// distinct from 15434 (costdist) and 15436 (notifier) — to avoid colliding
// with those tests if the full (non -short) suite ever runs them
// concurrently.
func TestClaimJobsBoundedClaimAheadWindow_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded:    true,
		EmbeddedDir: t.TempDir(),
		Port:        15437,
		RunMode:     "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("cjpgorg", "Claim Jobs PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("cjpg-worker", "Claim Jobs PG Worker")
	_, err = dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)

	svc := checkjobsvc.NewService(dbSvc.DB())
	now := time.Now()

	newJob := func(slug string, scheduledAt time.Time, period time.Duration) *models.CheckJob {
		check := models.NewCheck(org.UID, slug, "sleep")
		check.Enabled = false // avoid the auto-created check_job interfering
		r.NoError(dbSvc.CreateCheck(ctx, check))

		job := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(period))
		job.Type = "sleep"
		job.ScheduledAt = &scheduledAt
		job.EffectiveScheduledAt = &scheduledAt
		r.NoError(dbSvc.CreateCheckJob(ctx, job))

		return job
	}

	containsUID := func(jobs []*models.CheckJob, uid string) bool {
		for _, j := range jobs {
			if j.UID == uid {
				return true
			}
		}

		return false
	}

	// Short-period job (1m) scheduled 40s out: outside its own 30s clamp
	// window even though the flat FetchMaxAhead (5m) would admit it.
	tooFarJob := newJob("cjpg-toofar", now.Add(40*time.Second), time.Minute)

	// Short-period job (1m) scheduled 10s out: inside its own 30s clamp.
	withinJob := newJob("cjpg-within", now.Add(10*time.Second), time.Minute)

	// Long-period job (10m) scheduled 20s out: period/2=5m dominated by the
	// 30s floor, still well inside it.
	longPeriodJob := newJob("cjpg-longperiod", now.Add(20*time.Second), 10*time.Minute)

	// Due-now job: must always be claimed regardless of period.
	dueNowJob := newJob("cjpg-duenow", now.Add(-5*time.Second), time.Minute)

	jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
	r.NoError(err)

	r.False(containsUID(jobs, tooFarJob.UID),
		"a 1-minute-period job 40s out must not be claimed on Postgres either (window clamps to 30s)")
	r.True(containsUID(jobs, withinJob.UID),
		"a 1-minute-period job 10s out is inside its own 30s window on Postgres")
	r.True(containsUID(jobs, longPeriodJob.UID),
		"a 10-minute-period job 20s out is inside the 30s floor on Postgres")
	r.True(containsUID(jobs, dueNowJob.UID),
		"a due-now job must always be claimed regardless of period on Postgres")
}
