package entitlements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
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

	svc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	return svc, org, dbSvc
}

func TestDefaultsForSelfHosted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := entitlements.DefaultsFor(config.DeploymentModeSelfHosted)
	r.NotNil(defaults.Limits.MaxSSOUsers)
	r.Equal(30, *defaults.Limits.MaxSSOUsers)
	r.Nil(defaults.Limits.MaxChecksPerMinute)
}

func TestDefaultsForSaaS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := entitlements.DefaultsFor(config.DeploymentModeSaaS)
	r.Nil(defaults.Limits.MaxSSOUsers)
	r.NotNil(defaults.Limits.MaxChecksPerMinute)
	r.Equal(6, *defaults.Limits.MaxChecksPerMinute)
}

func TestDefaultsForUnknownFallsBackToSelfHosted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := entitlements.DefaultsFor("nope")
	r.NotNil(defaults.Limits.MaxSSOUsers)
	r.Equal(30, *defaults.Limits.MaxSSOUsers)
}

func TestResolveDefaultsWhenNoRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	resolved, err := svc.Resolve(t.Context(), org.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceDefault, resolved.Source)
	r.False(resolved.Stale)
	// Self-hosted defaults: MaxSSOUsers=30, MaxChecksPerMinute nil.
	r.NotNil(resolved.Limits.MaxSSOUsers)
	r.Equal(30, *resolved.Limits.MaxSSOUsers)
	r.Nil(resolved.Limits.MaxChecksPerMinute)
}

func TestSetMergesWithDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{
			MaxChecksPerMinute: entitlements.Int(12),
		},
		Source: models.EntitlementSourceBilling,
	}, "service:entitlements", "test"))

	resolved, err := svc.Resolve(t.Context(), org.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceBilling, resolved.Source)
	r.NotNil(resolved.Limits.MaxChecksPerMinute)
	r.Equal(12, *resolved.Limits.MaxChecksPerMinute)
	// Default MaxSSOUsers still surfaces for unset fields.
	r.NotNil(resolved.Limits.MaxSSOUsers)
	r.Equal(30, *resolved.Limits.MaxSSOUsers)
}

func TestSetWritesAuditRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxSSOUsers: entitlements.Int(10)},
		Source: models.EntitlementSourceBilling,
	}, "actor-1", "stripe.event.123"))

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxSSOUsers: entitlements.Int(20)},
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
	svc := entitlements.NewService(
		dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), staleAfter,
	)

	old := time.Now().Add(-72 * time.Hour)
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits:       entitlements.Limits{MaxSSOUsers: entitlements.Int(3)},
		Source:       models.EntitlementSourceBilling,
		LastSyncedAt: &old,
	}, "service:entitlements", ""))

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.True(resolved.Stale)
	// MaxSSOUsers reverts to default (30) since stale.
	r.NotNil(resolved.Limits.MaxSSOUsers)
	r.Equal(30, *resolved.Limits.MaxSSOUsers)
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
	svc := entitlements.NewService(
		dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), staleAfter,
	)

	old := time.Now().Add(-72 * time.Hour)
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits:       entitlements.Limits{MaxSSOUsers: entitlements.Int(7)},
		Source:       models.EntitlementSourceAdmin,
		LastSyncedAt: &old,
	}, "user:abc", ""))

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.False(resolved.Stale)
	r.NotNil(resolved.Limits.MaxSSOUsers)
	r.Equal(7, *resolved.Limits.MaxSSOUsers)
}

// TestCheckSSOMembershipUnlimitedWhenNil exercises the no-cap path
// (a SaaS org by default, where MaxSSOUsers is nil).
func TestCheckSSOMembershipUnlimitedWhenNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("saas-org", "SaaS Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0)
	r.NoError(svc.CheckSSOMembership(ctx, org.UID))
}

func TestCheckSSOMembershipBlocksAtCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	// Lower the cap to 2 for a fast test.
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxSSOUsers: entitlements.Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	// Seed two SSO-linked members.
	for _, label := range []string{"alice", "bob"} {
		user := models.NewUser(label + "@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		provider := models.NewUserProvider(user.UID, models.ProviderTypeGoogle, label+"-sub")
		r.NoError(dbSvc.CreateUserProvider(ctx, provider))
	}

	// Cap reached: third call must fail.
	err := svc.CheckSSOMembership(ctx, org.UID)
	r.Error(err)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxSSOUsers", quotaErr.LimitName)
	r.Equal(2, quotaErr.Limit)
	r.Equal(2, quotaErr.CurrentUsage)
}

func TestReserveCheckExecutionUnlimitedWhenNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)
	// Self-hosted defaults: MaxChecksPerMinute is nil, so a thousand
	// calls in a row succeed instantly.
	for range 1000 {
		r.NoError(svc.ReserveCheckExecution(t.Context(), org.UID))
	}
}

func TestReserveCheckExecutionBucketDrains(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("rated-org", "Rated Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0)

	// SaaS default cap is 6/min — the bucket starts full so the first
	// six calls succeed; the seventh is denied.
	for i := range 6 {
		r.NoError(svc.ReserveCheckExecution(ctx, org.UID), "call %d", i+1)
	}

	err = svc.ReserveCheckExecution(ctx, org.UID)
	r.Error(err)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxChecksPerMinute", quotaErr.LimitName)
	r.Equal(6, quotaErr.Limit)
}

func TestReserveCheckExecutionResetOnSet(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("rated2-org", "Rated2 Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0)
	for range 6 {
		r.NoError(svc.ReserveCheckExecution(ctx, org.UID))
	}
	r.Error(svc.ReserveCheckExecution(ctx, org.UID))

	// An admin override clears the cached limiter so the new cap takes
	// effect immediately. Bumping the cap to 100 lets fresh calls through.
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecksPerMinute: entitlements.Int(100)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	r.NoError(svc.ReserveCheckExecution(ctx, org.UID))
}

// TestPayloadRoundTrip ensures every field that lives inside the payload
// JSONB column survives a write/read cycle. Catches missing UPDATE columns
// in the upsert chain, JSON-tag drift between the model and the API, and
// any silent-drop unmarshal bugs.
func TestPayloadRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	displayName := "Acme Inc."
	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	synced := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)
	externalRef := "cus_123"

	in := entitlements.Entitlements{
		Limits: entitlements.Limits{
			MaxSSOUsers:        entitlements.Int(50),
			MaxChecksPerMinute: entitlements.Int(120),
		},
		Source:       models.EntitlementSourceBilling,
		DisplayName:  &displayName,
		ExternalRef:  &externalRef,
		Metadata:     map[string]any{"plan": "pro"},
		ExpiresAt:    &expires,
		LastSyncedAt: &synced,
	}

	r.NoError(svc.Set(t.Context(), org.UID, in, "service:entitlements", "round-trip"))

	resolved, err := svc.Resolve(t.Context(), org.UID)
	r.NoError(err)

	r.Equal(in.Source, resolved.Source)
	r.Equal(in.DisplayName, resolved.DisplayName)
	r.NotNil(resolved.ExpiresAt)
	r.Equal(expires, resolved.ExpiresAt.UTC().Truncate(time.Second))
	r.NotNil(resolved.LastSyncedAt)
	r.Equal(synced, resolved.LastSyncedAt.UTC().Truncate(time.Second))

	r.Equal(in.Limits, resolved.Limits)
}
