package checkjobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

const claimPluginType = "x-claimtest"

// createPluginJob creates a check of a plugin type whose job is due on region.
//
//nolint:revive // Test helper function, context parameter order is acceptable
func createPluginJob(
	t *testing.T, ctx context.Context, svc *sqlite.Service, orgUID string, due time.Time, region *string,
) *models.CheckJob {
	t.Helper()

	check := models.NewCheck(orgUID, "plugin-"+uuid.New().String()[:8], claimPluginType)
	check.Config = models.JSONMap{"host": "gopher.acme.com"}
	require.NoError(t, svc.CreateCheck(ctx, check))

	job := new(models.CheckJob)
	require.NoError(t, svc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))

	_, err := svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Set("region = ?", region).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)

	return job
}

// A plugin-type job is only claimed by a worker that advertises the type
// (spec 2026-10-17-05 §17); built-in jobs are unaffected.
//
//nolint:paralleltest // shares database state, like its siblings in this package
func TestPluginJobsOnlyReachCapableWorkers(t *testing.T) {
	r := require.New(t)

	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	due := time.Now().Add(-time.Minute)
	cloud := "eu-west-1"
	private := "@office"

	cloudHTTP := createTestCheckJob(t, ctx, dbSvc, org.UID, due, &cloud)
	cloudPlugin := createPluginJob(t, ctx, dbSvc, org.UID, due, &cloud)
	privatePlugin := createPluginJob(t, ctx, dbSvc, org.UID, due, &private)

	t.Run("cloud worker without the plugin", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, &cloud)

		claimed, _, err := svc.ClaimJobs(ctx, worker.UID, &cloud, 20, 20, 0, 5*time.Minute)
		r.NoError(err)
		r.False(jobUIDs(claimed)[cloudPlugin.UID], "a worker without the plugin claimed its job")
		r.True(jobUIDs(claimed)[cloudHTTP.UID], "positive control: the built-in job is claimable")
		r.NoError(svc.ReleaseLease(ctx, cloudHTTP.UID, worker.UID, due))

		express, err := svc.ClaimJobsForCheck(ctx, worker.UID, &cloud, cloudPlugin.CheckUID)
		r.NoError(err)
		r.Empty(express, "the express path must not claim it either")

		other := checkjobsvc.WithPluginTypes(ctx, []string{"x-other"})
		claimed, _, err = svc.ClaimJobs(other, worker.UID, &cloud, 20, 20, 0, 5*time.Minute)
		r.NoError(err)
		r.False(jobUIDs(claimed)[cloudPlugin.UID], "another plugin's type does not qualify")

		for _, job := range claimed {
			r.NoError(svc.ReleaseLease(ctx, job.UID, worker.UID, due))
		}
	})

	t.Run("cloud worker with the plugin", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, &cloud)
		capable := checkjobsvc.WithPluginTypes(ctx, []string{"x-other", claimPluginType})

		claimed, _, err := svc.ClaimJobs(capable, worker.UID, &cloud, 20, 20, 0, 5*time.Minute)
		r.NoError(err)
		r.True(jobUIDs(claimed)[cloudPlugin.UID], "a worker with the plugin claims its job")
		r.True(jobUIDs(claimed)[cloudHTTP.UID])

		for _, job := range claimed {
			r.NoError(svc.ReleaseLease(ctx, job.UID, worker.UID, due))
		}
	})

	t.Run("agents", func(t *testing.T) { //nolint:paralleltest // shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		scope := checkjobsvc.AgentScope{OrgUID: org.UID, Region: private}

		claimed, _, err := svc.ClaimJobsForAgent(ctx, worker.UID, scope, "", 20, 5*time.Minute)
		r.NoError(err)
		r.False(jobUIDs(claimed)[privatePlugin.UID], "an agent without the plugin claimed its job")

		scope.PluginTypes = checkjobsvc.PluginTypesFromCapabilities([]string{"ipv4", "ipv6", claimPluginType})
		r.Equal([]string{claimPluginType}, scope.PluginTypes)

		claimed, _, err = svc.ClaimJobsForAgent(ctx, worker.UID, scope, "", 20, 5*time.Minute)
		r.NoError(err)
		r.True(jobUIDs(claimed)[privatePlugin.UID], "an agent advertising the plugin claims its job")
	})
}
