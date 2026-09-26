package checkjobsvc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// Spec 2026-09-25-04: passive checks (heartbeat, email) are evaluated on the
// jobs node and nowhere else. These tests pin the claim side of that:
//
//   - no check worker claim (regular, express) and no agent claim (org or
//     system scope) ever returns a passive job, including a REGIONAL one that
//     escaped the migration — that is what closes "an agent turns every
//     evaluation into an error and an incident";
//   - ClaimPassiveJobs returns exactly the due, region-less passive jobs;
//   - two jobs-node claimers never both get the same job.

// createPassiveJob creates a passive check of the given type and returns its
// job, due at `due`. region non-nil rewrites the job onto that region, which
// is the shape a pre-migration (or escaped) row has.
//
//nolint:revive // Test helper function, context parameter order is acceptable
func createPassiveJob(
	t *testing.T, ctx context.Context, svc *sqlite.Service, orgUID string,
	checkType checkerdef.CheckType, due time.Time, region *string,
) *models.CheckJob {
	t.Helper()

	check := models.NewCheck(orgUID, "passive-"+uuid.New().String()[:8], string(checkType))
	check.Config = models.JSONMap{"token": uuid.NewString()}
	check.Regions = []string{"eu-west-1"} // dropped: a passive check has no regions
	require.NoError(t, svc.CreateCheck(ctx, check))
	require.Empty(t, check.Regions, "CreateCheck must drop a passive check's regions")

	job := new(models.CheckJob)
	require.NoError(t, svc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))
	require.Nil(t, job.Region, "a passive check owns one NULL-region job")

	_, err := svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Set("region = ?", region).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)

	job.ScheduledAt = &due
	job.Region = region

	return job
}

func jobUIDs(jobs []*models.CheckJob) map[string]bool {
	out := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		out[job.UID] = true
	}

	return out
}

//nolint:paralleltest // shares database state, like its siblings in this package
func TestPassiveJobsNeverReachCheckWorkersOrAgents(t *testing.T) {
	r := require.New(t)

	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	due := time.Now().Add(-time.Minute)
	cloud := "eu-west-1"
	private := "@office"

	// Positive controls: an active job in each scope, so an empty claim can
	// never be mistaken for "nothing was due".
	cloudHTTP := createTestCheckJob(t, ctx, dbSvc, org.UID, due, &cloud)
	privateHTTP := createTestCheckJob(t, ctx, dbSvc, org.UID, due, &private)

	passive := []*models.CheckJob{
		createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeHeartbeat, due, nil),
		createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeEmail, due, nil),
		// Escaped the migration: still pinned to a region.
		createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeHeartbeat, due, &cloud),
		createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeHeartbeat, due, &private),
		createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeEmail, due, &private),
	}

	assertNoPassive := func(name string, claimed []*models.CheckJob) {
		t.Helper()

		got := jobUIDs(claimed)
		for _, job := range passive {
			r.Falsef(got[job.UID], "%s claimed passive job %s (region %v)", name, job.UID, job.Region)
		}
	}

	t.Run("cloud worker", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, &cloud)

		claimed, _, err := svc.ClaimJobs(ctx, worker.UID, &cloud, 20, 20, 5*time.Minute)
		r.NoError(err)
		assertNoPassive("cloud worker", claimed)
		r.True(jobUIDs(claimed)[cloudHTTP.UID], "positive control: the cloud http job is claimable")
		r.NoError(svc.ReleaseLease(ctx, cloudHTTP.UID, worker.UID, due))
	})

	t.Run("region-less cloud worker", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)

		claimed, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 20, 20, 5*time.Minute)
		r.NoError(err)
		assertNoPassive("region-less cloud worker", claimed)
		r.True(jobUIDs(claimed)[cloudHTTP.UID], "positive control: the cloud http job is claimable")
		r.NoError(svc.ReleaseLease(ctx, cloudHTTP.UID, worker.UID, due))
	})

	t.Run("express claim", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, &cloud)

		for _, job := range passive {
			claimed, err := svc.ClaimJobsForCheck(ctx, worker.UID, &cloud, job.CheckUID)
			r.NoError(err)
			assertNoPassive("express claim", claimed)
		}
	})

	t.Run("org agent", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		scope := checkjobsvc.AgentScope{OrgUID: org.UID, Region: private}

		claimed, _, err := svc.ClaimJobsForAgent(ctx, worker.UID, scope, "", 20, 5*time.Minute)
		r.NoError(err)
		assertNoPassive("org agent", claimed)
		r.True(jobUIDs(claimed)[privateHTTP.UID], "positive control: the private http job is claimable")
		r.NoError(svc.ReleaseLease(ctx, privateHTTP.UID, worker.UID, due))

		// Pinned to one passive check (the agent express path).
		for _, job := range passive {
			pinned, _, pinErr := svc.ClaimJobsForAgent(ctx, worker.UID, scope, job.CheckUID, 20, 5*time.Minute)
			r.NoError(pinErr)
			assertNoPassive("org agent (pinned)", pinned)
		}
	})

	t.Run("system agent", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, &cloud)
		scope := checkjobsvc.AgentScope{Region: cloud, System: true}

		claimed, _, err := svc.ClaimJobsForAgent(ctx, worker.UID, scope, "", 20, 5*time.Minute)
		r.NoError(err)
		assertNoPassive("system agent", claimed)
		r.True(jobUIDs(claimed)[cloudHTTP.UID], "positive control: the cloud http job is claimable")
		r.NoError(svc.ReleaseLease(ctx, cloudHTTP.UID, worker.UID, due))
	})
}

//nolint:paralleltest // shares database state, like its siblings in this package
func TestClaimPassiveJobsScope(t *testing.T) {
	r := require.New(t)

	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	worker := createTestWorker(t, ctx, dbSvc, nil)

	now := time.Now()
	due := now.Add(-time.Second)
	cloud := "eu-west-1"

	heartbeat := createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeHeartbeat, due, nil)
	email := createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeEmail, due, nil)
	future := createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeHeartbeat, now.Add(20*time.Second), nil)
	regional := createPassiveJob(t, ctx, dbSvc, org.UID, checkerdef.CheckTypeHeartbeat, due, &cloud)
	active := createTestCheckJob(t, ctx, dbSvc, org.UID, due, nil)

	claimed, nextIn, err := svc.ClaimPassiveJobs(ctx, worker.UID, 10)
	r.NoError(err)

	got := jobUIDs(claimed)
	r.Len(claimed, 2, "exactly the due, region-less passive jobs")
	r.True(got[heartbeat.UID])
	r.True(got[email.UID])
	r.False(got[future.UID], "not due yet: no claim-ahead on the jobs node")
	r.False(got[regional.UID], "a regional passive row is healed by the boot repair, not claimed")
	r.False(got[active.UID], "an active check is never the jobs node's")

	for _, job := range claimed {
		r.NotNil(job.Check, "the check is attached for the first-signal grace")
		r.Equal(worker.UID, *job.LeaseWorkerUID)
	}

	r.Greater(nextIn, 10*time.Second, "the hint points at the future job's tick")
	r.LessOrEqual(nextIn, 20*time.Second)

	again, _, err := svc.ClaimPassiveJobs(ctx, worker.UID, 10)
	r.NoError(err)
	r.Empty(again, "a leased job is not claimed twice")
}

// TestClaimPassiveJobsTwoClaimers is the "two jobs nodes" case: both claim the
// same due jobs at the same time, and every job lands with exactly one of
// them.
//
//nolint:paralleltest // shares database state, like its siblings in this package
func TestClaimPassiveJobsTwoClaimers(t *testing.T) {
	r := require.New(t)

	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	due := time.Now().Add(-time.Second)

	const jobCount = 12

	want := make(map[string]bool, jobCount)

	for i := range jobCount {
		checkType := checkerdef.CheckTypeHeartbeat
		if i%2 == 1 {
			checkType = checkerdef.CheckTypeEmail
		}

		want[createPassiveJob(t, ctx, dbSvc, org.UID, checkType, due, nil).UID] = true
	}

	nodes := []*models.Worker{
		createTestWorker(t, ctx, dbSvc, nil),
		createTestWorker(t, ctx, dbSvc, nil),
	}

	var (
		mu     sync.Mutex
		counts = make(map[string]int, jobCount)
		wg     sync.WaitGroup
	)

	for _, node := range nodes {
		wg.Add(1)

		go func(workerUID string) {
			defer wg.Done()

			// Small batches, repeated, so the two claimers interleave.
			for range jobCount {
				claimed, _, err := svc.ClaimPassiveJobs(context.Background(), workerUID, 2)
				if err != nil {
					continue // an SQLite optimistic-lock miss: the other node won
				}

				mu.Lock()
				for _, job := range claimed {
					counts[job.UID]++
				}
				mu.Unlock()
			}
		}(node.UID)
	}

	wg.Wait()

	r.Len(counts, jobCount, "every due passive job was claimed")

	for uid, n := range counts {
		r.True(want[uid])
		r.Equalf(1, n, "job %s was claimed %d times", uid, n)
	}
}

func TestPassiveCheckTypesMatchesIsPassive(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, name := range checkerdef.PassiveCheckTypes() {
		r.Truef(checkerdef.CheckType(name).IsPassive(), "%s is listed as passive but IsPassive disagrees", name)
	}

	// Every registered type IsPassive accepts is listed, and nothing more:
	// heartbeat, email and the private-location monitor (spec 2026-09-25-05).
	passive := 0

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		if checkType.IsPassive() {
			passive++
		}
	}

	r.Len(checkerdef.PassiveCheckTypes(), passive)
	r.Len(checkerdef.PassiveCheckTypes(), 3)
	r.True(checkerdef.CheckTypePrivateLocation.IsPassive())
	r.False(checkerdef.CheckTypeHTTP.IsPassive())
}
