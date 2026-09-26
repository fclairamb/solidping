package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// setupDockerGateService builds a checks.Service backed by an in-memory
// SQLite DB, with no entitlement caps (nil entSvc — quota is not what this
// suite exercises), and sets the given deployment mode the way app/server.go
// wires it in production (spec 2026-09-25-22).
func setupDockerGateService(t *testing.T, deploymentMode string) (*checks.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("docker-gate-org", "Docker Gate Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), nil)
	svc.SetDeploymentMode(deploymentMode)

	return svc, org
}

// dockerCheckReq builds a minimally-valid docker check request, optionally
// pinned to explicit regions.
func dockerCheckReq(regions []string) checks.CreateCheckRequest {
	return checks.CreateCheckRequest{
		Type:    "docker",
		Config:  map[string]any{"containerName": "web", "host": "unix:///var/run/docker.sock"},
		Regions: regions,
	}
}

// TestCreateDockerCheck_SaaS_RejectsSharedPlacement covers the spec's first
// Tests bullet: in SaaS mode, creating a docker check with no region pin (so
// it would auto-place onto shared regions) is a 400 VALIDATION_ERROR, and the
// message names the actual constraint.
func TestCreateDockerCheck_SaaS_RejectsSharedPlacement(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSaaS)

	_, err := svc.CreateCheck(ctx, org.Slug, dockerCheckReq(nil))
	r.Error(err)
	r.ErrorContains(err, "docker checks are not available on this deployment")
}

// TestCreateDockerCheck_SaaS_RejectsExplicitSharedRegion covers the same rule
// when the caller pins an explicit (non-private) region rather than leaving
// placement automatic.
func TestCreateDockerCheck_SaaS_RejectsExplicitSharedRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSaaS)

	_, err := svc.CreateCheck(ctx, org.Slug, dockerCheckReq([]string{"eu-west"}))
	r.Error(err)
	r.ErrorContains(err, "docker checks are not available on this deployment")
}

// TestCreateDockerCheck_SaaS_AllowsPrivateOnlyPlacement is the spec's carved-
// out exception: a docker check pinned exclusively to the org's own private
// (agent-hosted) region(s) is allowed even in SaaS mode — the supported path
// for a SaaS customer who wants Docker monitoring.
func TestCreateDockerCheck_SaaS_AllowsPrivateOnlyPlacement(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSaaS)

	created, err := svc.CreateCheck(ctx, org.Slug, dockerCheckReq([]string{"@office"}))
	r.NoError(err)
	r.Equal([]string{"@office"}, created.Regions)
}

// TestCreateDockerCheck_SelfHosted_AllowsSharedPlacement proves self-hosted is
// entirely unaffected: docker against the local daemon is the feature there,
// with no region restriction.
func TestCreateDockerCheck_SelfHosted_AllowsSharedPlacement(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)

	created, err := svc.CreateCheck(ctx, org.Slug, dockerCheckReq(nil))
	r.NoError(err)
	r.NotEmpty(created.UID)
}

// TestUpdateDockerCheck_SaaS_RejectsMovingOffPrivateRegion proves the same
// gate applies on PATCH, not only on create: a private-only docker check
// cannot be re-pinned onto a shared region while SaaS mode is active.
func TestUpdateDockerCheck_SaaS_RejectsMovingOffPrivateRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSaaS)

	created, err := svc.CreateCheck(ctx, org.Slug, dockerCheckReq([]string{"@office"}))
	r.NoError(err)

	newRegions := []string{"eu-west"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Regions: &newRegions})
	r.Error(err)
	r.ErrorContains(err, "docker checks are not available on this deployment")
}
