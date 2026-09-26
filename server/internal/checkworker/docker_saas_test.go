package checkworker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newDockerGateRunner builds an in-process worker configured with the given
// deployment mode and node role (spec 2026-09-25-22).
func newDockerGateRunner(t *testing.T, dbSvc db.Service, deploymentMode, nodeRole string) *CheckWorker {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			CheckWorker: config.CheckWorkerConfig{Nb: 2, FetchMaxAhead: 5 * time.Minute},
		},
		Deployment: config.DeploymentConfig{Mode: deploymentMode},
		Node:       config.NodeConfig{Role: nodeRole},
	}

	svcList := services.NewRegistry()
	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	svcList.CheckJobs = checkJobSvc

	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })
	svcList.EventNotifier = eventNotifier

	return NewCheckWorker(dbSvc, cfg, svcList, checkJobSvc)
}

// runDockerJob creates a real `docker` check pointed at a unix socket that is
// guaranteed not to exist, claims its job, runs it through the real
// executeJob path and returns the persisted result. A socket path that never
// exists means any run that actually reaches the checker fails for ITS OWN
// reason (a dial/stat error), which is what lets the assertions below tell
// "blocked by the SaaS gate" apart from "executed, and failed to connect".
func runDockerJob(
	t *testing.T, ctx context.Context, runner *CheckWorker, dbSvc db.Service, //nolint:revive
) *models.Result {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("docker-gate-"+uuid.New().String()[:8], "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("dkw-"+uuid.New().String()[:8], "Docker Gate Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "docker-gate-"+uuid.New().String()[:8], string(checkerdef.CheckTypeDocker))
	check.Config = models.JSONMap{
		"containerName": "web",
		"host":          "unix:///should-never-be-dialed-2026-09-25-22.sock",
	}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	checkJob := new(models.CheckJob)
	r.NoError(dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))

	leaseExpiry := time.Now().Add(time.Minute)
	_, err = dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", worker.UID).
		Set("lease_expires_at = ?", leaseExpiry).
		Set("lease_starts = ?", 1).
		Where("uid = ?", checkJob.UID).
		Exec(ctx)
	r.NoError(err)

	checkJob.LeaseWorkerUID = &worker.UID
	checkJob.LeaseExpiresAt = &leaseExpiry
	checkJob.LeaseStarts = 1

	r.NoError(runner.executeJob(ctx, runner.logger, checkJob))

	result := new(models.Result)
	r.NoError(dbSvc.DB().NewSelect().Model(result).
		Where("check_uid = ?", check.UID).
		Order("created_at DESC").Limit(1).Scan(ctx))

	return result
}

// TestExecuteJob_DockerBlockedOnSaaSSharedWorker proves the runtime backstop
// (spec 2026-09-25-22): a docker job reaching a SaaS shared worker is refused
// with the exact gate message and, since the socket path is guaranteed not to
// exist, PROVES no dial was attempted — a real attempt would fail with a
// different (stat/dial) error instead.
func TestExecuteJob_DockerBlockedOnSaaSSharedWorker(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runner := newDockerGateRunner(t, dbSvc, config.DeploymentModeSaaS, "")

	result := runDockerJob(t, ctx, runner, dbSvc)
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusError), *result.Status, "output: %v", result.Output)
	r.Equal("docker checks are not available on this deployment", result.Output[checkerdef.OutputKeyError])
}

// TestExecuteJob_DockerAllowedOnAgentWorkerEvenInSaaS proves the guard keys on
// THIS PROCESS's role, not the check row or the deployment mode alone: a
// deported agent — the supported way to run docker checks under SaaS — keeps
// executing them. The nonexistent socket path means it still fails, but for
// its OWN reason, never the gate's.
func TestExecuteJob_DockerAllowedOnAgentWorkerEvenInSaaS(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runner := newDockerGateRunner(t, dbSvc, config.DeploymentModeSaaS, config.NodeRoleAgent)

	result := runDockerJob(t, ctx, runner, dbSvc)
	r.NotNil(result.Status)
	r.NotEqual("docker checks are not available on this deployment", result.Output[checkerdef.OutputKeyError],
		"an agent-mode worker must reach the checker, not the SaaS gate")
}

// TestExecuteJob_DockerAllowedOnSelfHostedSharedWorker proves self-hosted is
// entirely unaffected: a shared (non-agent) self-hosted worker still executes
// docker checks — monitoring the local daemon is the feature there.
func TestExecuteJob_DockerAllowedOnSelfHostedSharedWorker(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runner := newDockerGateRunner(t, dbSvc, config.DeploymentModeSelfHosted, "")

	result := runDockerJob(t, ctx, runner, dbSvc)
	r.NotNil(result.Status)
	r.NotEqual("docker checks are not available on this deployment", result.Output[checkerdef.OutputKeyError],
		"self-hosted must reach the checker, not the SaaS gate")
}
