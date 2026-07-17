package checkworker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

const tunnelSSHCheckUID = "ssh-bastion-uid"

// dialerObservingChecker records whether a tunnel dialer reached the checker,
// and reports the status it is told to.
type dialerObservingChecker struct {
	checkType  checkerdef.CheckType
	sawDialer  bool
	status     checkerdef.Status
	dialTarget string
	dialErr    error
}

func (c *dialerObservingChecker) Type() checkerdef.CheckType { return c.checkType }

func (c *dialerObservingChecker) Validate(_ *checkerdef.CheckSpec) error { return nil }

func (c *dialerObservingChecker) Execute(
	ctx context.Context, _ checkerdef.Config,
) (*checkerdef.Result, error) {
	dialer := checkerdef.TunnelDialerFrom(ctx)
	c.sawDialer = dialer != nil

	// Optionally exercise the dialer, so a bastion that refuses to forward is
	// observed exactly as a real checker would observe it.
	if dialer != nil && c.dialTarget != "" {
		conn, err := dialer.DialContext(ctx, "tcp", c.dialTarget)
		if err != nil {
			c.dialErr = err
		} else {
			_ = conn.Close()
		}
	}

	return &checkerdef.Result{Status: c.status, Metrics: map[string]any{"probe_ms": 1.0}}, nil
}

// setupTunnelJob creates an org, worker and a check of `checkType` whose config
// carries the given tunnel reference, and returns its claimed job.
func setupTunnelJob(
	t *testing.T, runner *CheckWorker, dbSvc *sqlite.Service, ctx context.Context, //nolint:revive
	checkType checkerdef.CheckType, config models.JSONMap,
) (*models.CheckJob, *models.Organization) {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "tunneled-"+uuid.New().String()[:8], string(checkType))
	check.Config = config
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	return checkJob, org
}

func lastResultForJob(t *testing.T, dbSvc *sqlite.Service, ctx context.Context, //nolint:revive
	checkJob *models.CheckJob,
) *models.Result {
	t.Helper()

	result := new(models.Result)
	require.NoError(t, dbSvc.DB().NewSelect().Model(result).
		Where("check_uid = ?", checkJob.CheckUID).
		Order("created_at DESC").Limit(1).Scan(ctx))

	return result
}

// A successful tunneled execution hands the checker a dialer and records the
// setup cost as its own metric — never folded into the check's duration, which
// would make every tunneled check's latency graph a picture of SSH handshakes
// rather than of the target.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_TunnelSetupIsMeasuredSeparately(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	const ttype = checkerdef.CheckType("test-tunneled")
	checker := &dialerObservingChecker{checkType: ttype, status: checkerdef.StatusUp}
	runner.getChecker, runner.parseConfig = stubResolvers(ttype, checker)

	srv := sshtunneltest.Start(t)
	ctx = sshtunnel.WithResolver(ctx, sshtunnel.NewResolver(
		&tunnelCheckLoader{config: srv.CheckConfig()}, nil,
	))

	checkJob, _ := setupTunnelJob(t, runner, dbSvc, ctx, ttype, models.JSONMap{
		checkerdef.TunnelCheckUIDConfigKey: tunnelSSHCheckUID,
	})

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	require.True(t, checker.sawDialer, "the checker must receive the tunnel dialer on its context")

	result := lastResultForJob(t, dbSvc, ctx, checkJob)
	require.Equal(t, int(checkerdef.StatusUp), *result.Status)
	require.Contains(t, result.Metrics, checkerdef.MetricKeyTunnelSetupMs)
	require.Contains(t, result.Metrics, "probe_ms", "the checker's own metrics survive")
}

// An untunneled check must not grow the metric, nor see a dialer.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_UntunneledCheckIsUnaffected(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	const ttype = checkerdef.CheckType("test-untunneled")
	checker := &dialerObservingChecker{checkType: ttype, status: checkerdef.StatusUp}
	runner.getChecker, runner.parseConfig = stubResolvers(ttype, checker)

	checkJob, _ := setupTunnelJob(t, runner, dbSvc, ctx, ttype, models.JSONMap{})

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	require.False(t, checker.sawDialer)

	result := lastResultForJob(t, dbSvc, ctx, checkJob)
	require.Equal(t, int(checkerdef.StatusUp), *result.Status)
	require.NotContains(t, result.Metrics, checkerdef.MetricKeyTunnelSetupMs)
}

// A tunnel that cannot be established produces a DISTINCT result — the marker
// plus a "tunnel failed" message naming the bastion — and the target probe never
// runs. That distinction is what later lets alerting suppress dependents of a
// broken bastion instead of paging for every service behind it.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_TunnelFailureProducesDistinctResult(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	const ttype = checkerdef.CheckType("test-tunnel-broken")
	checker := &dialerObservingChecker{checkType: ttype, status: checkerdef.StatusUp}
	runner.getChecker, runner.parseConfig = stubResolvers(ttype, checker)

	// A bastion whose fingerprint does not match: resolution fails.
	srv := sshtunneltest.Start(t)
	config := srv.CheckConfig()
	config["expected_fingerprint"] = "SHA256:not-the-real-one"

	ctx = sshtunnel.WithResolver(ctx, sshtunnel.NewResolver(&tunnelCheckLoader{config: config}, nil))

	checkJob, _ := setupTunnelJob(t, runner, dbSvc, ctx, ttype, models.JSONMap{
		checkerdef.TunnelCheckUIDConfigKey: tunnelSSHCheckUID,
	})

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	require.False(t, checker.sawDialer, "the target probe must not run when the tunnel is down")

	result := lastResultForJob(t, dbSvc, ctx, checkJob)
	require.Equal(t, int(checkerdef.StatusError), *result.Status)
	require.Equal(t, true, result.Output[checkerdef.OutputKeyTunnelFailed])
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "tunnel failed")
	require.Equal(t, tunnelSSHCheckUID, result.Output[checkerdef.TunnelCheckUIDConfigKey])
	// Even a failed tunnel reports how long it took to fail.
	require.Contains(t, result.Metrics, checkerdef.MetricKeyTunnelSetupMs)
}

// The bastion refusing to forward at all is re-classified as a tunnel failure,
// even though the checker only ever saw "the dial failed".
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_ForwardRejectionIsClassifiedAsTunnelFailure(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	const ttype = checkerdef.CheckType("test-tunnel-prohibited")
	checker := &dialerObservingChecker{
		checkType: ttype, status: checkerdef.StatusDown, dialTarget: "private.invalid:80",
	}
	runner.getChecker, runner.parseConfig = stubResolvers(ttype, checker)

	srv := sshtunneltest.Start(t)
	srv.Reject("private.invalid:80", sshtunneltest.Rejection{
		Reason: ssh.Prohibited, Message: "administratively prohibited",
	})

	ctx = sshtunnel.WithResolver(ctx, sshtunnel.NewResolver(
		&tunnelCheckLoader{config: srv.CheckConfig()}, nil,
	))

	checkJob, _ := setupTunnelJob(t, runner, dbSvc, ctx, ttype, models.JSONMap{
		checkerdef.TunnelCheckUIDConfigKey: tunnelSSHCheckUID,
	})

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))
	require.Error(t, checker.dialErr)

	result := lastResultForJob(t, dbSvc, ctx, checkJob)
	require.Equal(t, int(checkerdef.StatusError), *result.Status)
	require.Equal(t, true, result.Output[checkerdef.OutputKeyTunnelFailed])
}

// Without a resolver wired at all, a tunneled job fails loudly as a tunnel
// failure rather than panicking or probing untunneled.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_TunnelWithoutResolverFails(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	const ttype = checkerdef.CheckType("test-tunnel-unwired")
	checker := &dialerObservingChecker{checkType: ttype, status: checkerdef.StatusUp}
	runner.getChecker, runner.parseConfig = stubResolvers(ttype, checker)

	checkJob, _ := setupTunnelJob(t, runner, dbSvc, ctx, ttype, models.JSONMap{
		checkerdef.TunnelCheckUIDConfigKey: tunnelSSHCheckUID,
	})

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))
	require.False(t, checker.sawDialer)

	result := lastResultForJob(t, dbSvc, ctx, checkJob)
	require.Equal(t, int(checkerdef.StatusError), *result.Status)
	require.Equal(t, true, result.Output[checkerdef.OutputKeyTunnelFailed])
}

// tunnelCheckLoader serves a single SSH check row to the resolver.
type tunnelCheckLoader struct {
	config map[string]any
}

func (l *tunnelCheckLoader) GetCheck(_ context.Context, orgUID, checkUID string) (*models.Check, error) {
	if checkUID != tunnelSSHCheckUID {
		return nil, errNoSuchCheck
	}

	slug := "bastion"

	return &models.Check{
		UID: checkUID, OrganizationUID: orgUID, Slug: &slug,
		Type: string(checkerdef.CheckTypeSSH), Config: l.config,
	}, nil
}

var errNoSuchCheck = errors.New("no such check")
