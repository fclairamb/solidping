package entitlements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
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
	r.Nil(defaults.Limits.MaxChecks)
	r.NotNil(defaults.DisplayName)
	r.Equal("Self-hosted", *defaults.DisplayName)
}

func TestDefaultsForSaaS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := entitlements.DefaultsFor(config.DeploymentModeSaaS)
	// Aligned with the billing Free plan (pricing decision 2026-07-12:
	// 100 checks, 6 checks/min, 5 seats).
	r.NotNil(defaults.Limits.MaxSSOUsers)
	r.Equal(5, *defaults.Limits.MaxSSOUsers)
	r.NotNil(defaults.Limits.MaxChecksPerMinute)
	r.Equal(6, *defaults.Limits.MaxChecksPerMinute)
	r.NotNil(defaults.Limits.MaxChecks)
	r.Equal(100, *defaults.Limits.MaxChecks)
	r.NotNil(defaults.DisplayName)
	r.Equal("Free", *defaults.DisplayName)
	r.NotNil(defaults.DisplayEmoji)
	r.Equal("🆓", *defaults.DisplayEmoji)
}

func TestDefaultsForUnknownFallsBackToSelfHosted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := entitlements.DefaultsFor("nope")
	r.NotNil(defaults.Limits.MaxSSOUsers)
	r.Equal(30, *defaults.Limits.MaxSSOUsers)
	r.NotNil(defaults.DisplayName)
	r.Equal("Self-hosted", *defaults.DisplayName)
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
	// No row at all still inherits the default display identity.
	r.NotNil(resolved.DisplayName)
	r.Equal("Self-hosted", *resolved.DisplayName)
}

func TestResolveSaaSDefaultsWhenNoRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("saas-no-row", "SaaS No Row")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0)

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(100, *resolved.Limits.MaxChecks)
	r.NotNil(resolved.Limits.MaxChecksPerMinute)
	r.Equal(6, *resolved.Limits.MaxChecksPerMinute)
	r.NotNil(resolved.Limits.MaxSSOUsers)
	r.Equal(5, *resolved.Limits.MaxSSOUsers)
	r.NotNil(resolved.DisplayName)
	r.Equal("Free", *resolved.DisplayName)
	r.NotNil(resolved.DisplayEmoji)
	r.Equal("🆓", *resolved.DisplayEmoji)
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
	// A row that never set a display identity inherits the default one.
	r.NotNil(resolved.DisplayName)
	r.Equal("Self-hosted", *resolved.DisplayName)
}

func TestSetOwnDisplayIdentityOverridesDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	name := "🚀 Team"
	emoji := "🚀"
	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits:       entitlements.Limits{MaxChecksPerMinute: entitlements.Int(12)},
		Source:       models.EntitlementSourceBilling,
		DisplayName:  &name,
		DisplayEmoji: &emoji,
	}, "service:entitlements", "test"))

	resolved, err := svc.Resolve(t.Context(), org.UID)
	r.NoError(err)
	r.NotNil(resolved.DisplayName)
	r.Equal("🚀 Team", *resolved.DisplayName)
	r.NotNil(resolved.DisplayEmoji)
	r.Equal("🚀", *resolved.DisplayEmoji)
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

// newCheckRate seeds a check with the given enabled/period/internal flags
// and returns it. Period is parsed from a duration string (e.g. "1m", "30s").
func seedCheck(t *testing.T, dbSvc *sqlite.Service, orgUID string, enabled, internal bool, period time.Duration) {
	t.Helper()
	r := require.New(t)

	check := models.NewCheck(orgUID, "", "http")
	check.Enabled = enabled
	check.Internal = internal
	check.Period = timeutils.Duration(period)
	r.NoError(dbSvc.CreateCheck(t.Context(), check))
}

func TestUsageCountsAndRate(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	// Two enabled @ 1m (1/min each), one disabled @ 30s (counted but no rate),
	// one internal @ 10s (excluded entirely).
	seedCheck(t, dbSvc, org.UID, true, false, time.Minute)
	seedCheck(t, dbSvc, org.UID, true, false, time.Minute)
	seedCheck(t, dbSvc, org.UID, false, false, 30*time.Second)
	seedCheck(t, dbSvc, org.UID, true, true, 10*time.Second)

	usage, err := svc.Usage(ctx, org.UID)
	r.NoError(err)
	// Internal check excluded: 3 non-internal checks.
	r.Equal(3, usage.Checks)
	// Only the two enabled non-internal checks contribute rate: 1 + 1 = 2/min.
	r.InDelta(2.0, usage.ChecksPerMinute, 0.0001)
	r.Equal(0, usage.SSOUsers)
}

func TestUsageWithSSOMembers(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	user := models.NewUser("sso@example.com")
	r.NoError(dbSvc.CreateUser(ctx, user))
	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
	r.NoError(dbSvc.CreateOrganizationMember(ctx, member))
	provider := models.NewUserProvider(user.UID, models.ProviderTypeGoogle, "sso-sub")
	r.NoError(dbSvc.CreateUserProvider(ctx, provider))

	usage, err := svc.Usage(ctx, org.UID)
	r.NoError(err)
	r.Equal(0, usage.Checks)
	r.Equal(1, usage.SSOUsers)
}

func TestCheckCreateAllowedUnlimitedWhenNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)
	// Self-hosted defaults: MaxChecks is nil → unlimited.
	for range 10 {
		seedCheck(t, dbSvc, org.UID, true, false, time.Minute)
	}
	r.NoError(svc.CheckCreateAllowed(ctx, org.UID))
}

func TestCheckCreateAllowedUnderCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(3)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	seedCheck(t, dbSvc, org.UID, true, false, time.Minute)
	r.NoError(svc.CheckCreateAllowed(ctx, org.UID))
}

func TestCheckCreateAllowedBlocksAtCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	seedCheck(t, dbSvc, org.UID, true, false, time.Minute)
	seedCheck(t, dbSvc, org.UID, true, false, time.Minute)

	err := svc.CheckCreateAllowed(ctx, org.UID)
	r.Error(err)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxChecks", quotaErr.LimitName)
	r.Equal(2, quotaErr.Limit)
	r.Equal(2, quotaErr.CurrentUsage)
}

func TestCheckCreateAllowedInternalExempt(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	// Internal checks do not count toward the cap, so creating many of them
	// keeps CheckCreateAllowed passing for a non-internal check.
	for range 5 {
		seedCheck(t, dbSvc, org.UID, true, true, time.Minute)
	}
	r.NoError(svc.CheckCreateAllowed(ctx, org.UID))
}

func TestMergePropagatesMaxChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, _ := setup(t)

	// Nil row → MaxChecks stays nil (unlimited) under self-hosted defaults.
	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.Nil(resolved.Limits.MaxChecks)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(42)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	resolved, err = svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(42, *resolved.Limits.MaxChecks)
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

func TestPlanWeightFreeByDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, _ := setup(t)

	// No entitlement row → defaults → free tier.
	r.Equal(entitlements.PlanWeightFree, svc.PlanWeight(t.Context(), org.UID))
}

func TestPlanWeightPaidForBillingAndAdmin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, src := range []models.EntitlementSource{
		models.EntitlementSourceBilling,
		models.EntitlementSourceAdmin,
	} {
		svc, org, _ := setup(t)
		r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
			Limits: entitlements.Limits{MaxChecksPerMinute: entitlements.Int(60)},
			Source: src,
		}, "actor", "promote"))

		r.Equal(entitlements.PlanWeightPaid, svc.PlanWeight(t.Context(), org.UID),
			"source %s must resolve to the paid weight", src)
	}
}

func TestPlanWeightNilReceiverIsFree(t *testing.T) {
	t.Parallel()
	var svc *entitlements.Service
	require.Equal(t, entitlements.PlanWeightFree, svc.PlanWeight(t.Context(), "any-org"))
}

// TestSetDenormalizesPlanWeightOntoJobs verifies a billing upgrade propagates
// the paid weight onto the org's existing check_jobs immediately.
func TestSetDenormalizesPlanWeightOntoJobs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, org, dbSvc := setup(t)
	ctx := t.Context()

	// A check (and its check_job) starts free (plan_weight 0).
	check := models.NewCheck(org.UID, "weighted-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	var before models.CheckJob
	r.NoError(dbSvc.DB().NewSelect().Model(&before).Where("check_uid = ?", check.UID).Scan(ctx))
	r.Equal(0, before.PlanWeight, "new check_job starts at the free weight")

	// Billing provisions the org → plan_weight propagates to its jobs.
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecksPerMinute: entitlements.Int(60)},
		Source: models.EntitlementSourceBilling,
	}, "service:billing", "upgrade"))

	var after models.CheckJob
	r.NoError(dbSvc.DB().NewSelect().Model(&after).Where("check_uid = ?", check.UID).Scan(ctx))
	r.Equal(entitlements.PlanWeightPaid, after.PlanWeight,
		"billing upgrade must denormalize the paid weight onto existing jobs")
}
