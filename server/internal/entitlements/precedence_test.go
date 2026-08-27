package entitlements_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// saasSetup is the self-hosted `setup` helper with SaaS defaults, so a
// released org falls back to something with real numbers in it — the state the
// precedence tests care about is "what does an org resolve to with no row".
func saasSetup(t *testing.T) (*entitlements.Service, *models.Organization, *sqlite.Service) {
	t.Helper()

	_, org, dbSvc := setup(t)
	svc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0)

	return svc, org, dbSvc
}

// TestBillingPushOntoAdminRowIsSuppressed is the heart of the spec: an admin
// override wins until it is explicitly released. The push is ACCEPTED (no
// error — billing must not error-loop) but changes nothing, and says so.
func TestBillingPushOntoAdminRowIsSuppressed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	outcome, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{
			MaxChecks:          entitlements.Int(5000),
			MaxChecksPerMinute: entitlements.Int(600),
		},
	}, "superadmin:alice", "incident bump")
	r.NoError(err)
	r.True(outcome.Applied)

	outcome, err = svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceBilling,
		Limits: entitlements.Limits{
			MaxChecks:          entitlements.Int(100),
			MaxChecksPerMinute: entitlements.Int(10),
		},
	}, "service:entitlements", "")
	r.NoError(err, "a suppressed push must not be an error")
	r.False(outcome.Applied)
	r.Equal(models.EntitlementSourceAdmin, outcome.SuppressedBy)

	// The stored limits are untouched.
	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceAdmin, resolved.Source)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(5000, *resolved.Limits.MaxChecks)
	r.NotNil(resolved.Limits.MaxChecksPerMinute)
	r.Equal(600, *resolved.Limits.MaxChecksPerMinute)
}

// TestSuppressedPushIsAudited proves the discarded push leaves a trace — the
// whole reason a suppression is safe to do silently to billing is that it is
// not silent to us.
func TestSuppressedPushIsAudited(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, dbSvc := saasSetup(t)
	ctx := t.Context()

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(5000)},
	}, "superadmin:alice", "")
	r.NoError(err)

	_, err = svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceBilling,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(100)},
	}, "service:entitlements", "")
	r.NoError(err)

	audits, err := dbSvc.ListOrgEntitlementAudits(ctx, models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: org.UID,
	})
	r.NoError(err)
	r.Len(audits, 2, "the admin write and the suppressed push")
	r.Equal(entitlements.AuditSourceBillingSuppressed, audits[0].Source)
	r.NotNil(audits[0].Reason)
	r.Contains(*audits[0].Reason, "admin override")
	r.Contains(audits[0].AfterSnapshot, "suppressedAttempt",
		"the audit must keep what billing wanted, not only what stayed")
}

// TestReleaseLetsTheNextBillingPushApply walks the whole release contract:
// after a release the org resolves to deployment defaults, and the next
// billing push is applied normally.
func TestReleaseLetsTheNextBillingPushApply(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(5000)},
	}, "superadmin:alice", "")
	r.NoError(err)

	released, err := svc.Release(ctx, org.UID, "superadmin:alice", "back to billing")
	r.NoError(err)
	r.True(released)

	// Defaults apply when no billing row exists after the release.
	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceDefault, resolved.Source)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(100, *resolved.Limits.MaxChecks, "SaaS free defaults, not the released 5000")

	// And billing drives it again.
	outcome, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceBilling,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(2000)},
	}, "service:entitlements", "")
	r.NoError(err)
	r.True(outcome.Applied, "a released org accepts billing again")

	resolved, err = svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceBilling, resolved.Source)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(2000, *resolved.Limits.MaxChecks)
}

// TestReleaseWithoutAnOverrideIsANoOp keeps a double-click from erroring.
func TestReleaseWithoutAnOverrideIsANoOp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)

	released, err := svc.Release(t.Context(), org.UID, "superadmin:alice", "")
	r.NoError(err)
	r.False(released)
}

// TestBillingPushOntoBillingRowStillApplies is the negative control for the
// precedence rule: it must gate on the row being ADMIN-sourced, not on there
// simply being a row.
func TestBillingPushOntoBillingRowStillApplies(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceBilling,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(100)},
	}, "service:entitlements", "")
	r.NoError(err)

	outcome, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceBilling,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(900)},
	}, "service:entitlements", "")
	r.NoError(err)
	r.True(outcome.Applied)

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(900, *resolved.Limits.MaxChecks)
}

// TestAdminWriteOverAdminRowApplies: the rule suppresses BILLING, never the
// superadmin editing their own override again.
func TestAdminWriteOverAdminRowApplies(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(5000)},
	}, "superadmin:alice", "")
	r.NoError(err)

	outcome, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(6000)},
	}, "superadmin:bob", "")
	r.NoError(err)
	r.True(outcome.Applied)

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(6000, *resolved.Limits.MaxChecks)
}

// TestAdminRowStillCountsAsPaid pins the scheduling side of the contract: an
// admin-sourced row is a real plan, so the cost-aware scheduler must keep
// protecting the org exactly as it would a billing-written one.
func TestAdminRowStillCountsAsPaid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	r.Equal(entitlements.PlanWeightFree, svc.PlanWeight(ctx, org.UID),
		"an org on defaults is free")

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(5000)},
	}, "superadmin:alice", "")
	r.NoError(err)

	r.Equal(entitlements.PlanWeightPaid, svc.PlanWeight(ctx, org.UID),
		"an admin override is a plan, not a default")

	// Releasing it drops the org back to free — the mirror of the promotion.
	_, err = svc.Release(ctx, org.UID, "superadmin:alice", "")
	r.NoError(err)
	r.Equal(entitlements.PlanWeightFree, svc.PlanWeight(ctx, org.UID))
}
