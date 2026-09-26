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
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestClaimPassiveJobsConcurrentClaimers_Postgres is the Postgres half of the
// "two jobs nodes" guarantee (spec 2026-09-25-04). SQLite serializes every
// claim on its single connection; Postgres is where two nodes really race, and
// FOR UPDATE SKIP LOCKED plus the lease written in the same transaction is
// what must hand each due job to exactly one of them. It also pins the
// exclusion on the cloud and agent claims against real Postgres SQL.
//
// Port 15517 is unused by every other embedded-Postgres test in the repo.
func TestClaimPassiveJobsConcurrentClaimers_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     15517,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	org := models.NewOrganization("passivepg", "Passive PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := checkjobsvc.NewService(dbSvc.DB())
	due := time.Now().Add(-time.Second)

	const jobCount = 30

	want := make(map[string]bool, jobCount)

	for i := range jobCount {
		checkType := checkerdef.CheckTypeHeartbeat
		if i%2 == 1 {
			checkType = checkerdef.CheckTypeEmail
		}

		check := models.NewCheck(org.UID, "passive-"+uuid.New().String()[:8], string(checkType))
		check.Config = models.JSONMap{"token": uuid.NewString()}
		check.Regions = []string{"eu-west-1"}
		r.NoError(dbSvc.CreateCheck(ctx, check))
		r.Empty(check.Regions)

		job := new(models.CheckJob)
		r.NoError(dbSvc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))
		r.Nil(job.Region)

		_, err = dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("scheduled_at = ?", due).Set("effective_scheduled_at = ?", due).
			Where("uid = ?", job.UID).Exec(ctx)
		r.NoError(err)

		want[job.UID] = true
	}

	// Cloud and agent claims never see them.
	cloud := "eu-west-1"
	cloudWorker := models.NewWorker("passivepg-cloud", "cloud")
	cloudWorker.Region = &cloud
	_, err = dbSvc.DB().NewInsert().Model(cloudWorker).Exec(ctx)
	r.NoError(err)

	cloudClaimed, _, err := svc.ClaimJobs(ctx, cloudWorker.UID, &cloud, 50, 50, 5*time.Minute)
	r.NoError(err)
	r.Empty(cloudClaimed, "a cloud worker never claims a passive job")

	agentClaimed, _, err := svc.ClaimJobsForAgent(
		ctx, cloudWorker.UID, checkjobsvc.AgentScope{Region: cloud, System: true}, "", 50, 5*time.Minute)
	r.NoError(err)
	r.Empty(agentClaimed, "a system agent never claims a passive job")

	// Four jobs nodes race for them.
	var (
		mu     sync.Mutex
		counts = make(map[string]int, jobCount)
		wg     sync.WaitGroup
	)

	for i := range 4 {
		node := models.NewWorker("passivepg-jobs-"+string(rune('a'+i)), "jobs")
		_, err = dbSvc.DB().NewInsert().Model(node).Exec(ctx)
		r.NoError(err)

		wg.Add(1)

		go func(workerUID string) {
			defer wg.Done()

			for range jobCount {
				claimed, _, claimErr := svc.ClaimPassiveJobs(context.Background(), workerUID, 3)
				if claimErr != nil {
					continue
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
		r.Equalf(1, n, "job %s was claimed by %d jobs nodes", uid, n)
	}
}
