package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// disabledCreds builds a plaintext-fallback credentials service (no master
// key) backed by an in-memory DEK store, so CreateCheck can persist configs.
func disabledCreds(t *testing.T) credentials.Service {
	t.Helper()
	creds, err := credentials.NewService(nil, newMemDEKStore())
	require.NoError(t, err)

	return creds
}

// setupQuotaService builds a checks service wired to a real entitlements
// service backed by an in-memory SQLite DB, plus an org capped at maxChecks.
func setupQuotaService(t *testing.T, maxChecks int) (*checks.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("quota-org", "Quota Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
		Limits: entcore.Limits{MaxChecks: entcore.Int(maxChecks)},
		Source: models.EntitlementSourceAdmin,
	}, "user:test", ""))

	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, org
}

func httpCheckReq() checks.CreateCheckRequest {
	return checks.CreateCheckRequest{
		Type:   "http",
		Config: map[string]any{"url": "https://example.com"},
	}
}

func TestCreateCheckBlockedOverCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupQuotaService(t, 1)

	// First check fits under the cap.
	_, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	// Second check hits the cap of 1 → quota error.
	_, err = svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.Error(err)
	r.ErrorIs(err, entcore.ErrEntitlementExceeded)

	var qe *entcore.QuotaError
	r.ErrorAs(err, &qe)
	r.Equal("MaxChecks", qe.LimitName)
}

func TestCreateCheckInternalBypassesCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupQuotaService(t, 1)

	// One non-internal check uses up the cap.
	_, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	// Internal checks bypass the cap entirely.
	internal := true
	req := httpCheckReq()
	req.Internal = &internal
	for range 3 {
		_, err = svc.CreateCheck(ctx, org.Slug, req)
		r.NoError(err)
	}
}

func TestCreateCheckUnlimitedWhenNoCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("nolimit-org", "No Limit Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Self-hosted defaults leave MaxChecks nil → unlimited.
	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	for range 5 {
		_, createErr := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
		r.NoError(createErr)
	}
}
