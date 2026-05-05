package entitlements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

func setup(t *testing.T) (*entitlements.Service, *models.Organization, *sqlite.Service) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("ent-org", "Ent Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := entitlements.NewService(dbSvc, entitlements.DefaultEntitlements, 0)

	return svc, org, dbSvc
}

func TestResolveDefaultsWhenNoRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	resolved, err := svc.Resolve(t.Context(), org.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceDefault, resolved.Source)
	r.False(resolved.Stale)
	// Defaults: max_checks is nil (unlimited), retention_days_raw=30.
	r.Nil(resolved.Limits.MaxChecks)
	r.NotNil(resolved.Limits.RetentionDaysRaw)
	r.Equal(30, *resolved.Limits.RetentionDaysRaw)
}

func TestSetMergesWithDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{
			MaxChecks: entitlements.Int(5),
		},
		Features: entitlements.Features{
			SSO: entitlements.Bool(false),
		},
		Source: models.EntitlementSourceBilling,
	}, "service:entitlements", "test"))

	resolved, err := svc.Resolve(t.Context(), org.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceBilling, resolved.Source)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(5, *resolved.Limits.MaxChecks)
	r.NotNil(resolved.Features.SSO)
	r.False(*resolved.Features.SSO)
	// Defaults still apply for unset fields.
	r.NotNil(resolved.Features.MCP)
	r.True(*resolved.Features.MCP)
}

func TestSetWritesAuditRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(10)},
		Source: models.EntitlementSourceBilling,
	}, "actor-1", "stripe.event.123"))

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(20)},
		Source: models.EntitlementSourceBilling,
	}, "actor-2", "stripe.event.456"))

	rows, err := dbSvc.ListOrgEntitlementAudits(t.Context(), models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: org.UID,
	})
	r.NoError(err)
	r.Len(rows, 2)
	// Order is newest first.
	r.Equal("actor-2", rows[0].Actor)
	r.NotNil(rows[0].Reason)
	r.Equal("stripe.event.456", *rows[0].Reason)

	// First row had no previous snapshot — bun may round-trip nil as an
	// empty JSON object on some backends, so treat both as "no snapshot".
	r.Empty(rows[1].BeforeSnapshot)
	r.NotNil(rows[1].AfterSnapshot)
	r.NotEmpty(rows[1].AfterSnapshot)
	// Second row carries the prior snapshot.
	r.NotEmpty(rows[0].BeforeSnapshot)
}

func TestStaleFallsBackToDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("stale-org", "Stale Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	staleAfter := 24 * time.Hour
	svc := entitlements.NewService(dbSvc, entitlements.DefaultEntitlements, staleAfter)

	old := time.Now().Add(-72 * time.Hour)
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits:       entitlements.Limits{MaxChecks: entitlements.Int(3)},
		Source:       models.EntitlementSourceBilling,
		LastSyncedAt: &old,
	}, "service:entitlements", ""))

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.True(resolved.Stale)
	// MaxChecks reverts to default (nil = unlimited) since stale.
	r.Nil(resolved.Limits.MaxChecks)
}

func TestStaleDoesNotApplyToAdminOverride(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("admin-org", "Admin Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	staleAfter := 24 * time.Hour
	svc := entitlements.NewService(dbSvc, entitlements.DefaultEntitlements, staleAfter)

	old := time.Now().Add(-72 * time.Hour)
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits:       entitlements.Limits{MaxChecks: entitlements.Int(7)},
		Source:       models.EntitlementSourceAdmin,
		LastSyncedAt: &old,
	}, "user:abc", ""))

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.False(resolved.Stale)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(7, *resolved.Limits.MaxChecks)
}

func TestFeatureEnabledStubReturnsDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	enabled, err := svc.FeatureEnabled(t.Context(), org.UID, "mcp")
	r.NoError(err)
	r.True(enabled)

	enabled, err = svc.FeatureEnabled(t.Context(), org.UID, "priority_support")
	r.NoError(err)
	r.False(enabled)

	_, err = svc.FeatureEnabled(t.Context(), org.UID, "nonexistent_feature")
	r.ErrorIs(err, entitlements.ErrUnknownFeature)
}

func TestCanCreateStubAlwaysAllows(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	// PR-1 stub: no enforcement. Calling with any resource name returns
	// nil (allow). Enforcement PRs replace this with real logic.
	r.NoError(svc.CanCreate(t.Context(), org.UID, "checks"))
	r.NoError(svc.CanCreate(t.Context(), org.UID, "anything-goes"))
}
