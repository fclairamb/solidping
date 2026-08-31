package entitlements_test

import (
	"sync"
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
	r.NotNil(defaults.Limits.MaxUsers)
	r.Equal(30, *defaults.Limits.MaxUsers)
	r.Nil(defaults.Limits.MaxChecksPerMinute)
	r.Nil(defaults.Limits.MaxChecks)
	// Self-hosted keeps the "free private locations" competitive
	// positioning: unlimited deported agents.
	r.Nil(defaults.Limits.MaxDeportedAgents)
	r.NotNil(defaults.DisplayName)
	r.Equal("Self-hosted", *defaults.DisplayName)
}

func TestDefaultsForSaaS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := entitlements.DefaultsFor(config.DeploymentModeSaaS)
	// Aligned with the billing Free plan (pricing decision 2026-07-12:
	// 100 checks, 5 seats; check rate raised to 10/min on 2026-08-15).
	r.NotNil(defaults.Limits.MaxUsers)
	r.Equal(5, *defaults.Limits.MaxUsers)
	r.NotNil(defaults.Limits.MaxChecksPerMinute)
	r.Equal(10, *defaults.Limits.MaxChecksPerMinute)
	r.NotNil(defaults.Limits.MaxChecks)
	r.Equal(100, *defaults.Limits.MaxChecks)
	// Mirrors the Free SKU's private-location agent cap (ladder 1/3/6/9).
	r.NotNil(defaults.Limits.MaxDeportedAgents)
	r.Equal(1, *defaults.Limits.MaxDeportedAgents)
	r.NotNil(defaults.DisplayName)
	r.Equal("Free", *defaults.DisplayName)
	r.NotNil(defaults.DisplayEmoji)
	r.Equal("🆓", *defaults.DisplayEmoji)
}

// TestUsageChecksPerMinuteCountsRegions verifies that a multi-region check
// contributes (60s/period) × region_count to ChecksPerMinute (spec
// 2026-07-20-05): every selected region runs the check every period.
func TestUsageChecksPerMinuteCountsRegions(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, dbSvc := setup(t)

	mkCheck := func(slug string, period time.Duration, regions []string) {
		check := models.NewCheck(org.UID, slug, "http")
		check.Period = timeutils.Duration(period)
		check.Regions = regions
		check.Config = models.JSONMap{"url": "https://example.com"}
		r.NoError(dbSvc.CreateCheck(ctx, check))
	}

	// 1m check, no regions → 1.0/min (min region count is 1).
	mkCheck("no-regions", time.Minute, nil)
	// 1m check, 3 regions → 3.0/min.
	mkCheck("three-regions", time.Minute, []string{"default", "eu-2", "us-1"})
	// 30s check, 2 regions → (60/30) × 2 = 4.0/min.
	mkCheck("two-regions-30s", 30*time.Second, []string{"default", "eu-2"})

	usage, err := svc.Usage(ctx, org.UID)
	r.NoError(err)
	r.Equal(3, usage.Checks)
	r.InDelta(8.0, usage.ChecksPerMinute, 0.001,
		"1 (no-region) + 3 (3 regions) + 4 (2 regions @30s) = 8 checks/minute")
}

func TestDefaultsForUnknownFallsBackToSelfHosted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := entitlements.DefaultsFor("nope")
	r.NotNil(defaults.Limits.MaxUsers)
	r.Equal(30, *defaults.Limits.MaxUsers)
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
	// Self-hosted defaults: MaxUsers=30, MaxChecksPerMinute nil.
	r.NotNil(resolved.Limits.MaxUsers)
	r.Equal(30, *resolved.Limits.MaxUsers)
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
	r.Equal(10, *resolved.Limits.MaxChecksPerMinute)
	r.NotNil(resolved.Limits.MaxUsers)
	r.Equal(5, *resolved.Limits.MaxUsers)
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
	// Default MaxUsers still surfaces for unset fields.
	r.NotNil(resolved.Limits.MaxUsers)
	r.Equal(30, *resolved.Limits.MaxUsers)
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
		Limits: entitlements.Limits{MaxUsers: entitlements.Int(10)},
		Source: models.EntitlementSourceBilling,
	}, "actor-1", "stripe.event.123"))

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxUsers: entitlements.Int(20)},
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
		Limits:       entitlements.Limits{MaxUsers: entitlements.Int(3)},
		Source:       models.EntitlementSourceBilling,
		LastSyncedAt: &old,
	}, "service:entitlements", ""))

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.True(resolved.Stale)
	// MaxUsers reverts to default (30) since stale.
	r.NotNil(resolved.Limits.MaxUsers)
	r.Equal(30, *resolved.Limits.MaxUsers)
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
		Limits:       entitlements.Limits{MaxUsers: entitlements.Int(7)},
		Source:       models.EntitlementSourceAdmin,
		LastSyncedAt: &old,
	}, "user:abc", ""))

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.False(resolved.Stale)
	r.NotNil(resolved.Limits.MaxUsers)
	r.Equal(7, *resolved.Limits.MaxUsers)
}

// TestCheckMembershipUnlimitedWhenNil exercises the no-cap path
// (a SaaS org by default, where MaxUsers is nil).
func TestCheckMembershipUnlimitedWhenNil(t *testing.T) {
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
	r.NoError(svc.CheckMembership(ctx, org.UID))
}

func TestCheckMembershipBlocksAtCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	// Lower the cap to 2 for a fast test.
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxUsers: entitlements.Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	// Seed two members WITHOUT any user_providers row. The cap now counts
	// every member regardless of how they joined (SSO, invitation, email),
	// so provider-less members must still count.
	for _, label := range []string{"alice", "bob"} {
		user := models.NewUser(label + "@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))
	}

	// Cap reached: third call must fail.
	err := svc.CheckMembership(ctx, org.UID)
	r.Error(err)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxUsers", quotaErr.LimitName)
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

	// SaaS default cap is 10/min — the bucket starts full so the first
	// ten calls succeed; the eleventh is denied.
	for i := range 10 {
		r.NoError(svc.ReserveCheckExecution(ctx, org.UID), "call %d", i+1)
	}

	err = svc.ReserveCheckExecution(ctx, org.UID)
	r.Error(err)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxChecksPerMinute", quotaErr.LimitName)
	r.Equal(10, quotaErr.Limit)
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
	for range 10 {
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

// seedAgent mints a one-shot enrollment token bound to (orgUID, region) and
// immediately consumes it, returning the newly-active agent row. tokenHash
// must be unique per call within a test (no real crypto needed at this
// db-level layer, mirroring handlers/agents/service_test.go's fixtures).
func seedAgent(t *testing.T, dbSvc *sqlite.Service, orgUID, region, tokenHash string) *models.Agent {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	tok := models.NewAgentEnrollmentToken(orgUID, region, tokenHash, time.Now().Add(time.Hour), nil)
	r.NoError(dbSvc.CreateAgentEnrollmentToken(ctx, tok))

	agent, err := dbSvc.EnrollAgent(
		ctx, tokenHash, "agent-"+tokenHash, "ed-"+tokenHash, "age1"+tokenHash, "fp-"+tokenHash,
	)
	r.NoError(err)

	return agent
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

// TestUsageAgentsExcludesRevoked verifies Usage.Agents counts only active
// agents — a revoked one drops out of the count immediately.
func TestUsageAgentsExcludesRevoked(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	region := "@" + org.Slug + "/dc1"
	a1 := seedAgent(t, dbSvc, org.UID, region, "h1")
	seedAgent(t, dbSvc, org.UID, region, "h2")
	seedAgent(t, dbSvc, org.UID, "@"+org.Slug+"/dc2", "h3")

	usage, err := svc.Usage(ctx, org.UID)
	r.NoError(err)
	r.Equal(3, usage.Agents)

	r.NoError(dbSvc.RevokeAgent(ctx, org.UID, a1.UID))

	usage, err = svc.Usage(ctx, org.UID)
	r.NoError(err)
	r.Equal(2, usage.Agents)
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

// seedSLO inserts one live objective scoped to a freshly created check, so the
// maxSlos counter has something real to count.
func seedSLO(t *testing.T, dbSvc *sqlite.Service, orgUID, slug string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	check := models.NewCheck(orgUID, slug+"-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	objective := models.NewSLO(orgUID, slug, slug, 99.9)
	objective.CheckUID = &check.UID
	r.NoError(dbSvc.CreateSLO(ctx, objective))
}

func TestSloCreateAllowedUnlimitedWhenNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	// Self-hosted defaults: MaxSlos is nil → unlimited.
	seedSLO(t, dbSvc, org.UID, "one")
	seedSLO(t, dbSvc, org.UID, "two")
	seedSLO(t, dbSvc, org.UID, "three")
	r.NoError(svc.SloCreateAllowed(ctx, org.UID))
}

func TestSloCreateAllowedUnderCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxSlos: entitlements.Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	seedSLO(t, dbSvc, org.UID, "one")
	r.NoError(svc.SloCreateAllowed(ctx, org.UID))
}

func TestSloCreateAllowedBlocksAtCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxSlos: entitlements.Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	seedSLO(t, dbSvc, org.UID, "one")
	seedSLO(t, dbSvc, org.UID, "two")

	err := svc.SloCreateAllowed(ctx, org.UID)
	r.Error(err)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxSlos", quotaErr.LimitName)
	r.Equal(2, quotaErr.Limit)
	r.Equal(2, quotaErr.CurrentUsage)
}

// A soft cap must free a slot the moment an objective is deleted — the
// standard "delete one to add one" recovery path.
func TestSloCreateAllowedDeletedSloFreesASlot(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxSlos: entitlements.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	seedSLO(t, dbSvc, org.UID, "one")
	r.Error(svc.SloCreateAllowed(ctx, org.UID))

	objective, err := dbSvc.GetSLOBySlug(ctx, org.UID, "one")
	r.NoError(err)
	r.NoError(dbSvc.DeleteSLO(ctx, org.UID, objective.UID))

	r.NoError(svc.SloCreateAllowed(ctx, org.UID))
}

func TestAgentCreateAllowedUnlimitedWhenNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	region := "@" + org.Slug + "/dc1"
	// Self-hosted defaults: MaxDeportedAgents is nil → unlimited.
	seedAgent(t, dbSvc, org.UID, region, "h1")
	seedAgent(t, dbSvc, org.UID, region, "h2")
	seedAgent(t, dbSvc, org.UID, region, "h3")
	r.NoError(svc.AgentCreateAllowed(ctx, org.UID))
}

func TestAgentCreateAllowedUnderCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxDeportedAgents: entitlements.Int(3)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	seedAgent(t, dbSvc, org.UID, "@"+org.Slug+"/dc1", "h1")
	r.NoError(svc.AgentCreateAllowed(ctx, org.UID))
}

func TestAgentCreateAllowedBlocksAtCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxDeportedAgents: entitlements.Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	region := "@" + org.Slug + "/dc1"
	seedAgent(t, dbSvc, org.UID, region, "h1")
	seedAgent(t, dbSvc, org.UID, region, "h2")

	err := svc.AgentCreateAllowed(ctx, org.UID)
	r.Error(err)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxDeportedAgents", quotaErr.LimitName)
	r.Equal(2, quotaErr.Limit)
	r.Equal(2, quotaErr.CurrentUsage)
}

// TestAgentCreateAllowedRevokedAgentsDontCountAgainstCap verifies a revoked
// agent frees a slot immediately — the standard soft-cap "delete one to add
// one" recovery path.
func TestAgentCreateAllowedRevokedAgentsDontCountAgainstCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxDeportedAgents: entitlements.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	region := "@" + org.Slug + "/dc1"
	a1 := seedAgent(t, dbSvc, org.UID, region, "h1")

	// At cap: the next enrollment is blocked.
	r.Error(svc.AgentCreateAllowed(ctx, org.UID))

	r.NoError(dbSvc.RevokeAgent(ctx, org.UID, a1.UID))

	// Revoking frees the slot.
	r.NoError(svc.AgentCreateAllowed(ctx, org.UID))
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
			MaxUsers:           entitlements.Int(50),
			MaxChecksPerMinute: entitlements.Int(120),
			MaxDeportedAgents:  entitlements.Int(9),
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

	// Every limit the caller SET round-trips verbatim.
	r.Equal(in.Limits.MaxUsers, resolved.Limits.MaxUsers)
	r.Equal(in.Limits.MaxChecksPerMinute, resolved.Limits.MaxChecksPerMinute)
	r.Equal(in.Limits.MaxDeportedAgents, resolved.Limits.MaxDeportedAgents)

	// Absent numeric limits stay nil = unlimited...
	r.Nil(resolved.Limits.MaxChecks)
	r.Nil(resolved.Limits.MaxCustomDomains)
	r.Nil(resolved.Limits.MaxSlos)

	// ...but `whiteLabel` is null-FILLED from the deployment default rather
	// than left nil, because nil on a boolean entitlement means "use the
	// default", not "unbounded" (spec 2026-08-21-07). The fixture runs
	// self-hosted, whose default is false.
	r.NotNil(resolved.Limits.WhiteLabel)
	r.False(*resolved.Limits.WhiteLabel)
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

// newRatedService builds an entitlements service on the shared DB with SaaS
// defaults (MaxChecksPerMinute = 10).
func newRatedService(dbSvc *sqlite.Service) *entitlements.Service {
	return entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0)
}

// setRateCap writes MaxChecksPerMinute as a superadmin override, the way the
// entitlements editor (or a billing push) does.
func setRateCap(t *testing.T, svc *entitlements.Service, orgUID string, perMinute int) {
	t.Helper()
	require.NoError(t, svc.Set(t.Context(), orgUID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxChecksPerMinute: entitlements.Int(perMinute)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))
}

// TestReserveCheckExecutionRaisedCapAppliesWithoutRestart reproduces the
// production pathology of spec 2026-08-29-10: the cap is raised by ANOTHER
// process (the API server / billing push), so the worker's own service never
// runs Set and never clears its cache. Before the fix the worker's bucket kept
// its creation-time capacity of 10 forever and skipped executions the org was
// entitled to, while reporting the new limit in the QuotaError.
func TestReserveCheckExecutionRaisedCapAppliesWithoutRestart(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("rate-raise-org", "Rate Raise Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Two independent processes over one database: the API server that performs
	// the write, and a deported check worker that only ever reads.
	apiSvc := newRatedService(dbSvc)
	workerSvc := newRatedService(dbSvc)

	// The worker builds (and drains) its bucket at the SaaS default of 10/min.
	for i := range 10 {
		r.NoError(workerSvc.ReserveCheckExecution(ctx, org.UID), "call %d", i+1)
	}
	r.ErrorIs(workerSvc.ReserveCheckExecution(ctx, org.UID), entitlements.ErrEntitlementExceeded)

	// The operator raises the cap to 100 through the API server. The worker
	// process is NOT restarted and never observes the Set.
	setRateCap(t, apiSvc, org.UID, 100)

	resolved, err := workerSvc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecksPerMinute)
	r.Equal(100, *resolved.Limits.MaxChecksPerMinute, "the worker resolves the new limit")

	// ...and must now honor it. These calls happen within milliseconds, so the
	// old capacity/60 refill could never supply them: passing this loop proves
	// the cached bucket adopted the raised capacity rather than staying at 10.
	for i := range 50 {
		r.NoError(workerSvc.ReserveCheckExecution(ctx, org.UID),
			"post-raise call %d must be allowed at the new cap without a restart", i+1)
	}
}

// TestReserveCheckExecutionLoweredCapClampsWithoutRestart covers the other
// direction: a cap lowered elsewhere must clamp the surplus a stale bucket is
// holding, so the org cannot burst past its new limit.
func TestReserveCheckExecutionLoweredCapClampsWithoutRestart(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("rate-lower-org", "Rate Lower Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	apiSvc := newRatedService(dbSvc)
	workerSvc := newRatedService(dbSvc)

	// Start the worker's bucket at a generous 100/min: one call materializes it
	// nearly full (99 tokens).
	setRateCap(t, apiSvc, org.UID, 100)
	r.NoError(workerSvc.ReserveCheckExecution(ctx, org.UID))

	// The plan is downgraded to 5/min elsewhere.
	setRateCap(t, apiSvc, org.UID, 5)

	// The stale surplus must be clamped to the new burst: five more calls at
	// most, then denial. Without the clamp the worker would happily spend the
	// ~99 tokens it was holding.
	for i := range 5 {
		r.NoError(workerSvc.ReserveCheckExecution(ctx, org.UID), "post-lower call %d", i+1)
	}

	err = workerSvc.ReserveCheckExecution(ctx, org.UID)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal(5, quotaErr.Limit)
}

// TestReserveCheckExecutionCapacityChangeIsRaceFree exercises the in-place
// capacity reconciliation from many goroutines at once (run under -race): the
// reserve path mutates the cached bucket under the same lock allow() takes, so
// a concurrent cap change must never be observed half-applied.
func TestReserveCheckExecutionCapacityChangeIsRaceFree(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("rate-race-org", "Rate Race Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	apiSvc := newRatedService(dbSvc)
	workerSvc := newRatedService(dbSvc)

	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 50 {
				// The verdict does not matter here — only that concurrent
				// reserves and cap changes stay memory-safe.
				_ = workerSvc.ReserveCheckExecution(ctx, org.UID)
			}
		}()
	}

	for _, capacity := range []int{100, 5, 250, 20} {
		setRateCap(t, apiSvc, org.UID, capacity)
	}

	wg.Wait()
}
