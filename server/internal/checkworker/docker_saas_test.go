package checkworker

import (
	"context"
	"fmt"
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

	return runGateJob(t, ctx, runner, dbSvc, checkerdef.CheckTypeDocker, models.JSONMap{
		"containerName": "web",
		"host":          "unix:///should-never-be-dialed-2026-09-25-22.sock",
	})
}

// runJSDockerJob is runDockerJob with the docker check nested in a js script
// (spec 2026-10-16-09): the script returns the sub-check's status and output
// verbatim, so the persisted result carries the sub-check's refusal or error.
func runJSDockerJob(
	t *testing.T, ctx context.Context, runner *CheckWorker, dbSvc db.Service, //nolint:revive
) *models.Result {
	t.Helper()

	return runGateJob(t, ctx, runner, dbSvc, checkerdef.CheckTypeJS, models.JSONMap{
		"script": `var r = solidping.check("docker", ` +
			`{ host: "unix:///should-never-be-dialed-2026-10-16-09.sock", containerName: "web" }); ` +
			`return { status: r.status, output: r.output };`,
	})
}

// runGateJob creates a check of checkType with cfg, claims its job, runs it
// through the real executeJob path and returns the persisted result.
func runGateJob(
	t *testing.T, ctx context.Context, runner *CheckWorker, dbSvc db.Service, //nolint:revive
	checkType checkerdef.CheckType, cfg models.JSONMap,
) *models.Result {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("docker-gate-"+uuid.New().String()[:8], "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("dkw-"+uuid.New().String()[:8], "Docker Gate Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "docker-gate-"+uuid.New().String()[:8], string(checkType))
	check.Config = cfg
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

// TestExecuteJob_JSDockerSubCheckBlockedOnSaaSSharedWorker is the regression of
// spec 2026-10-16-09: a js check whose script calls solidping.check("docker",
// …) on a SaaS shared worker is refused with the same gate message as a
// top-level docker job. The socket path is guaranteed not to exist, so getting
// the gate message (not a dial error) proves no dial was attempted.
//
//nolint:paralleltest // the worker constructor installs the package-level checkjs gates
func TestExecuteJob_JSDockerSubCheckBlockedOnSaaSSharedWorker(t *testing.T) {
	r := require.New(t)
	_, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runner := newDockerGateRunner(t, dbSvc, config.DeploymentModeSaaS, "")

	result := runJSDockerJob(t, ctx, runner, dbSvc)
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusError), *result.Status, "output: %v", result.Output)
	r.Equal("docker checks are not available on this deployment", result.Output[checkerdef.OutputKeyError])
}

// TestExecuteJob_JSDockerSubCheckAllowedOnAgentWorker is the control: an agent
// keeps running nested docker sub-checks exactly as it runs top-level ones.
// The nonexistent socket makes it fail, but for its OWN reason.
//
//nolint:paralleltest // the worker constructor installs the package-level checkjs gates
func TestExecuteJob_JSDockerSubCheckAllowedOnAgentWorker(t *testing.T) {
	r := require.New(t)
	_, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runner := newDockerGateRunner(t, dbSvc, config.DeploymentModeSaaS, config.NodeRoleAgent)

	result := runJSDockerJob(t, ctx, runner, dbSvc)
	r.NotNil(result.Status)
	r.NotEqual("docker checks are not available on this deployment", result.Output[checkerdef.OutputKeyError],
		"an agent-mode worker must reach the docker checker, not the SaaS gate")
	r.Contains(fmt.Sprint(result.Output[checkerdef.OutputKeyError]), "docker",
		"the sub-check must have run and failed on the missing socket: %v", result.Output)
}

// NewDeploymentGate refuses docker exactly where executeJob refuses a
// top-level docker job (SaaS, not an agent), and nothing else.
func TestNewDeploymentGate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode, role string
		blocked    bool
	}{
		{config.DeploymentModeSaaS, "", true},
		{config.DeploymentModeSaaS, config.NodeRoleAgent, false},
		{config.DeploymentModeSelfHosted, "", false},
		{config.DeploymentModeSelfHosted, config.NodeRoleAgent, false},
	} {
		cfg := &config.Config{
			Deployment: config.DeploymentConfig{Mode: tc.mode},
			Node:       config.NodeConfig{Role: tc.role},
		}
		gate := NewDeploymentGate(cfg)

		want := ""
		if tc.blocked {
			want = ErrDockerNotAvailableInSaaS.Error()
		}

		require.Equal(t, want, gate(checkerdef.CheckTypeDocker), "mode=%s role=%s", tc.mode, tc.role)
		require.Empty(t, gate(checkerdef.CheckTypeHTTP), "mode=%s role=%s", tc.mode, tc.role)
		require.Equal(t, tc.blocked, cfg.DockerChecksBlocked(), "mode=%s role=%s", tc.mode, tc.role)
	}
}
