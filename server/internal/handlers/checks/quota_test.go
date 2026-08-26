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
// The db service comes back too, so a test can create a check the way the
// SERVER does (straight through the db, bypassing the checks service) — that
// is the only way to get an internal check since spec 2026-08-27-01.
func setupQuotaService(t *testing.T, maxChecks int) (*checks.Service, *sqlite.Service, *models.Organization) {
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

	return svc, dbSvc, org
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

	svc, _, org := setupQuotaService(t, 1)

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

// The `internal: true` bypass this file used to assert (TestCreateCheckInternalBypassesCap)
// was the bug: it let any caller create checks that never counted against
// MaxChecks. Its replacement — the field is refused, and the cap still bites —
// lives in internal_flag_test.go (spec 2026-08-27-01).

func TestCloneCheckBlockedOverCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := setupQuotaService(t, 1)

	// First (non-internal) check fills the cap of 1.
	created, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	// Cloning it would create a second non-internal check → quota error.
	_, err = svc.CloneCheck(ctx, org.Slug, created.UID, &checks.CloneCheckRequest{})
	r.Error(err)
	r.ErrorIs(err, entcore.ErrEntitlementExceeded)

	var qe *entcore.QuotaError
	r.ErrorAs(err, &qe)
	r.Equal("MaxChecks", qe.LimitName)
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
