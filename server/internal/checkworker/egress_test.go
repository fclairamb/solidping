package checkworker

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

const egressInternalBody = "internal-only-body"

// newEgressRunner builds an in-process worker over any database backend.
func newEgressRunner(t *testing.T, dbSvc db.Service) *CheckWorker {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			CheckWorker: config.CheckWorkerConfig{Nb: 2, FetchMaxAhead: 5 * time.Minute},
		},
	}

	svcList := services.NewRegistry()
	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	svcList.CheckJobs = checkJobSvc

	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })
	svcList.EventNotifier = eventNotifier

	return NewCheckWorker(dbSvc, cfg, svcList, checkJobSvc)
}

// runEgressHTTPJob creates a REAL `http` check on the given backend, claims
// its job, executes it through executeJob with the given guard, and returns
// the persisted result.
func runEgressHTTPJob(
	t *testing.T, ctx context.Context, runner *CheckWorker, dbSvc db.Service, //nolint:revive
	guard *egress.Guard, url string,
) *models.Result {
	t.Helper()

	r := require.New(t)

	runner.egressGuard = guard

	org := models.NewOrganization("egress-"+uuid.New().String()[:8], "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("egw-"+uuid.New().String()[:8], "Egress Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "egress-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHTTP))
	check.Config = models.JSONMap{"url": url, "body_expect": egressInternalBody}
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

// assertEgressDeniedThenAllowed is the integration contract, shared by the
// SQLite and Postgres suites: an HTTP check on a loopback server fails with
// the egress error under a denying worker and succeeds under an allowing one.
func assertEgressDeniedThenAllowed(t *testing.T, ctx context.Context, dbSvc db.Service) { //nolint:revive
	t.Helper()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(egressInternalBody))
	}))
	t.Cleanup(server.Close)

	runner := newEgressRunner(t, dbSvc)

	denied := runEgressHTTPJob(t, ctx, runner, dbSvc, egress.New(false), server.URL)
	r.NotNil(denied.Status)
	r.Equal(int(checkerdef.StatusError), *denied.Status, "output: %v", denied.Output)
	r.Contains(denied.Output[checkerdef.OutputKeyError],
		"target resolves to a non-public address, denied by egress policy")
	r.Contains(denied.Output[checkerdef.OutputKeyError], egress.EnvAllowPrivate)
	r.Equal(true, denied.Output[egress.OutputKeyDenied])
	r.NotContains(denied.Output, "body")

	allowed := runEgressHTTPJob(t, ctx, runner, dbSvc, egress.New(true), server.URL)
	r.NotNil(allowed.Status)
	r.Equal(int(checkerdef.StatusUp), *allowed.Status, "output: %v", allowed.Output)
	r.NotContains(allowed.Output, egress.OutputKeyDenied)
}

//nolint:paralleltest // shares the process-level browser/js activation globals newCheckWorker installs
func TestExecuteJob_EgressPolicy_SQLite(t *testing.T) {
	_, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	assertEgressDeniedThenAllowed(t, ctx, dbSvc)
}

// The SSH bastion is itself a user-chosen host dialed from the worker's
// network: under a denying worker a loopback bastion is refused, and the
// result is the distinct tunnel-failure result carrying the policy message.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_EgressPolicyGuardsTheSSHBastion(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	runner.egressGuard = egress.New(false)

	const ttype = checkerdef.CheckType("test-tunnel-egress")
	checker := &dialerObservingChecker{checkType: ttype, status: checkerdef.StatusUp}
	runner.getChecker, runner.parseConfig = stubResolvers(ttype, checker)

	srv := sshtunneltest.Start(t)
	ctx = sshtunnel.WithResolver(ctx, sshtunnel.NewResolver(
		&tunnelCheckLoader{config: srv.CheckConfig()}, nil,
	))

	checkJob := setupTunnelJob(t, runner, dbSvc, ctx, ttype, models.JSONMap{
		checkerdef.TunnelCheckUIDConfigKey: tunnelSSHCheckUID,
	})

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))
	require.False(t, checker.sawDialer, "the probe never runs behind a refused bastion")

	result := lastResultForJob(t, dbSvc, ctx, checkJob)
	require.Equal(t, int(checkerdef.StatusError), *result.Status)
	require.Equal(t, true, result.Output[checkerdef.OutputKeyTunnelFailed])
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "denied by egress policy")
}

// Placement: the guard is built from the config of the process that dials.
// A SaaS shared worker denies, a deported agent allows, self-hosted allows,
// and the operator switch overrides both ways.
//
//nolint:paralleltest // newCheckWorker installs process-level browser/js activation globals
func TestNewCheckWorkerEgressPlacement(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		role     string
		explicit *bool
		enforce  bool
	}{
		{name: "saas shared checks worker", mode: config.DeploymentModeSaaS, role: config.NodeRoleChecks, enforce: true},
		{name: "saas all-in-one", mode: config.DeploymentModeSaaS, role: config.NodeRoleAll, enforce: true},
		{name: "saas private agent", mode: config.DeploymentModeSaaS, role: config.NodeRoleAgent, enforce: false},
		{name: "self-hosted", mode: config.DeploymentModeSelfHosted, role: config.NodeRoleAll, enforce: false},
		{
			name: "saas worker opened by the operator", mode: config.DeploymentModeSaaS,
			role: config.NodeRoleChecks, explicit: config.BoolPtr(true), enforce: false,
		},
		{
			name: "agent closed by its operator", mode: config.DeploymentModeSaaS,
			role: config.NodeRoleAgent, explicit: config.BoolPtr(false), enforce: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Deployment.Mode = tc.mode
			cfg.Node.Role = tc.role
			cfg.Egress.AllowPrivateTargets = tc.explicit

			var worker *CheckWorker
			if tc.role == config.NodeRoleAgent {
				worker = NewAgentCheckWorker(cfg, nil)
			} else {
				worker = newCheckWorker(cfg, nil)
			}

			require.NotNil(t, worker.EgressGuard())
			require.Equal(t, tc.enforce, worker.EgressGuard().Enforcing())
		})
	}
}

// Every execution context carries the worker's guard and a fresh recorder.
func TestWithEgressPutsTheGuardOnTheContext(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	runner := &CheckWorker{egressGuard: egress.New(false)}

	ctx, rec := runner.withEgress(t.Context())
	r.Same(runner.egressGuard, egress.FromContext(ctx))
	r.Nil(rec.Denied())

	_, err := egress.FromContext(ctx).Resolve(ctx, "127.0.0.1")
	r.ErrorIs(err, egress.ErrDenied)
	r.NotNil(rec.Denied(), "a refusal on the execution context is recorded")
}

// The normalization: whatever the checker made of the refusal, the result is
// an Error carrying the policy message, and nothing points a traceroute at
// the refused address.
func TestApplyEgressDenial(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	denied := &egress.DeniedError{Host: "metadata.acme.com"}

	result := &checkerdef.Result{
		Status: checkerdef.StatusDown,
		Output: map[string]any{checkerdef.OutputKeyError: "request failed: dial tcp: something", "url": "u"},
	}
	result.SetNetworkFailure(checkerdef.NewNetworkFailure(
		checkerdef.NetFailureConnectionRefused, "metadata.acme.com", "169.254.169.254", 80))
	result.Diagnostics.FailureResponse = &checkerdef.FailureResponse{}

	applyEgressDenial(result, denied)

	r.Equal(checkerdef.StatusError, result.Status)
	r.Equal(denied.Error(), result.Output[checkerdef.OutputKeyError])
	r.Equal(true, result.Output[egress.OutputKeyDenied])
	r.Equal("u", result.Output["url"], "the rest of the output is kept")
	r.Nil(result.Diagnostics.NetworkFailure, "no path trace towards a refused address")
	r.Nil(result.Diagnostics.FailureResponse)

	// No refusal, no change.
	untouched := &checkerdef.Result{Status: checkerdef.StatusUp}
	applyEgressDenial(untouched, nil)
	r.Equal(checkerdef.StatusUp, untouched.Status)
	r.Nil(untouched.Output)

	// A result with no output map still gets the message.
	bare := &checkerdef.Result{Status: checkerdef.StatusDown}
	applyEgressDenial(bare, denied)
	r.Equal(checkerdef.StatusError, bare.Status)
	r.NotEmpty(bare.Output[checkerdef.OutputKeyError])
}
