package app

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/entitlements"
)

// newSaaSSeedServer builds a minimal SaaS-mode Server around an in-memory
// SQLite DB — SeedSaaSEntitlements only touches dbService and config.
func newSaaSSeedServer(ctx context.Context, t *testing.T) (*Server, *sqlite.Service) {
	t.Helper()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{}
	cfg.Deployment.Mode = config.DeploymentModeSaaS

	return &Server{dbService: dbSvc, config: cfg}, dbSvc
}

// paramString reads back a seeded string system parameter ("" when absent).
func paramString(ctx context.Context, t *testing.T, dbSvc *sqlite.Service, key string) string {
	t.Helper()

	param, err := dbSvc.GetSystemParameter(ctx, key)
	require.NoError(t, err)
	if param == nil {
		return ""
	}
	value, _ := param.Value["value"].(string)

	return value
}

// TestSeedUpgradeTokenSecretAbsentLeavesParameterUntouched covers the "partial
// configuration stays fine" contract: with the env var unset, an existing
// parameter value (set earlier via the API) is not clobbered on the next boot.
func TestSeedUpgradeTokenSecretAbsentLeavesParameterUntouched(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	srv, dbSvc := newSaaSSeedServer(ctx, t)
	t.Setenv(envEntitlementsUpgradeTokenSecret, "")

	// Nothing set at all → parameter stays absent.
	r.NoError(srv.SeedSaaSEntitlements(ctx))
	r.Empty(paramString(ctx, t, dbSvc, entitlements.ParamBillingUpgradeTokenSecret))

	// A value set out of band survives a re-seed with the env var unset.
	r.NoError(dbSvc.SetSystemParameter(ctx, entitlements.ParamBillingUpgradeTokenSecret, "set-via-api", true))
	r.NoError(srv.SeedSaaSEntitlements(ctx))
	r.Equal("set-via-api", paramString(ctx, t, dbSvc, entitlements.ParamBillingUpgradeTokenSecret))
}

// TestSeedUpgradeTokenSecretPresentUpserts covers the seeding path.
func TestSeedUpgradeTokenSecretPresentUpserts(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	srv, dbSvc := newSaaSSeedServer(ctx, t)
	t.Setenv(envEntitlementsUpgradeTokenSecret, "dedicated-upgrade-token-secret")

	r.NoError(srv.SeedSaaSEntitlements(ctx))
	r.Equal("dedicated-upgrade-token-secret",
		paramString(ctx, t, dbSvc, entitlements.ParamBillingUpgradeTokenSecret))

	// A later env change upserts rather than duplicating.
	t.Setenv(envEntitlementsUpgradeTokenSecret, "rotated-secret")
	r.NoError(srv.SeedSaaSEntitlements(ctx))
	r.Equal("rotated-secret", paramString(ctx, t, dbSvc, entitlements.ParamBillingUpgradeTokenSecret))
}

// TestSeedSaaSEntitlementsNoOpOutsideSaaS pins that self-hosted installs never
// seed billing credentials.
func TestSeedSaaSEntitlementsNoOpOutsideSaaS(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	srv, dbSvc := newSaaSSeedServer(ctx, t)
	srv.config.Deployment.Mode = config.DeploymentModeSelfHosted
	t.Setenv(envEntitlementsUpgradeTokenSecret, "dedicated-upgrade-token-secret")

	r.NoError(srv.SeedSaaSEntitlements(ctx))
	r.Empty(paramString(ctx, t, dbSvc, entitlements.ParamBillingUpgradeTokenSecret))
}

// captureLogs swaps the default slog handler for the duration of the test and
// returns the accumulated output.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return buf
}

// TestSeedEqualBillingSecretsLogsErrorButBoots pins acceptance criterion 4:
// equal secrets are called out at ERROR, and startup still succeeds.
func TestSeedEqualBillingSecretsLogsErrorButBoots(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	srv, _ := newSaaSSeedServer(ctx, t)
	t.Setenv(envEntitlementsBillingSecret, "same-value")
	t.Setenv(envEntitlementsUpgradeTokenSecret, "same-value")

	buf := captureLogs(t)
	r.NoError(srv.SeedSaaSEntitlements(ctx), "an unrotated-but-valid secret must not strand the boot")

	logs := buf.String()
	r.Contains(logs, "level=ERROR")
	r.Contains(logs, entitlements.ParamBillingUpgradeTokenSecret)
	r.Contains(logs, "forgeable")
}

// TestSeedDistinctBillingSecretsLogsNoError is the positive control for the
// check above: distinct secrets must not produce the ERROR.
func TestSeedDistinctBillingSecretsLogsNoError(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	srv, _ := newSaaSSeedServer(ctx, t)
	t.Setenv(envEntitlementsBillingSecret, "bearer-value")
	t.Setenv(envEntitlementsUpgradeTokenSecret, "distinct-signing-value")

	buf := captureLogs(t)
	r.NoError(srv.SeedSaaSEntitlements(ctx))
	r.NotContains(buf.String(), "level=ERROR")
}

// TestSeedOnlyInboundSecretLogsNoEqualityError guards the degenerate case: one
// secret set and the other absent must not trip the equality check.
func TestSeedOnlyInboundSecretLogsNoEqualityError(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	srv, _ := newSaaSSeedServer(ctx, t)
	t.Setenv(envEntitlementsBillingSecret, "bearer-value")
	t.Setenv(envEntitlementsUpgradeTokenSecret, "")

	buf := captureLogs(t)
	r.NoError(srv.SeedSaaSEntitlements(ctx))
	r.NotContains(buf.String(), "level=ERROR")
}
