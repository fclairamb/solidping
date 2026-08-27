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

// TestAdminNullMeansUnlimitedOnSaaS is the gap the audit caught: on SaaS every
// default is a real number, so the ordinary null-fill merge turned the
// superadmin editor's "Unlimited" switch into a no-op — the stored null was
// overwritten by the SaaS default on the way back out, the toggle flipped
// itself off on the next refetch, and the org stayed capped while the UI
// claimed otherwise.
//
// An `admin` row is whole-row: nil means unlimited.
func TestAdminNullMeansUnlimitedOnSaaS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	// Sanity: with no row at all the SaaS defaults are non-nil. Without this,
	// the assertion below could pass on a deployment where nothing was capped.
	base, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(base.Limits.MaxChecks)
	r.Equal(100, *base.Limits.MaxChecks)

	_, err = svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{
			MaxChecks:          nil, // explicitly unlimited
			MaxChecksPerMinute: entitlements.Int(600),
		},
	}, "superadmin:alice", "")
	r.NoError(err)

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.Nil(resolved.Limits.MaxChecks,
		"an admin row's nil cap is UNLIMITED, not an invitation to re-apply the SaaS default")
	// POSITIVE CONTROL: a stated cap in the same row still resolves to itself,
	// so the assertion above is not passing because everything became nil.
	r.NotNil(resolved.Limits.MaxChecksPerMinute)
	r.Equal(600, *resolved.Limits.MaxChecksPerMinute)
}

// TestBillingNullStillInheritsTheDefault is the negative control for the rule
// above: the whole-row reading is scoped to `admin`. Billing pushes a partial
// plan and must keep getting the deployment default for everything its SKU
// says nothing about — changing that would silently uncap every SaaS org the
// moment billing omitted a field.
func TestBillingNullStillInheritsTheDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceBilling,
		Limits: entitlements.Limits{
			MaxChecks:          nil, // "the SKU does not mention checks"
			MaxChecksPerMinute: entitlements.Int(60),
		},
	}, "service:entitlements", "")
	r.NoError(err)

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecks, "a billing row's nil still means 'not stated'")
	r.Equal(100, *resolved.Limits.MaxChecks)
	r.NotNil(resolved.Limits.MaxChecksPerMinute)
	r.Equal(60, *resolved.Limits.MaxChecksPerMinute)
}

// TestOrgAdminNullStillInheritsTheDefault pins the same for the org-scoped
// door: it behaves exactly as it did before this spec, so a self-hosted
// operator's partial write does not become an accidental uncapping.
func TestOrgAdminNullStillInheritsTheDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceOrgAdmin,
		Limits: entitlements.Limits{MaxUsers: entitlements.Int(20)},
	}, "user:bob", "")
	r.NoError(err)

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(100, *resolved.Limits.MaxChecks)
	r.NotNil(resolved.Limits.MaxUsers)
	r.Equal(20, *resolved.Limits.MaxUsers)
}

// TestAdminRowKeepsTheDefaultWhiteLabel guards the exception inside the
// exception. WhiteLabel is a boolean: its nil cannot mean "unbounded", and per
// EntitlementLimits' own contract it means "use the deployment default". If
// whole-row mode read it as false, every admin override would silently revoke
// white-label on a deployment that grants it.
func TestAdminRowKeepsTheDefaultWhiteLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Self-hosted defaults white-label to true — nobody should have to pay to
	// unbrand their own instance.
	svc, org, _ := setup(t)
	ctx := t.Context()

	base, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(base.Limits.WhiteLabel)
	r.True(*base.Limits.WhiteLabel)

	_, err = svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(50)},
	}, "superadmin:alice", "")
	r.NoError(err)

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.WhiteLabel,
		"an admin override that only touched numbers must not revoke white-label")
	r.True(*resolved.Limits.WhiteLabel)

	// POSITIVE CONTROL: an explicit false is still honored.
	denied := false
	_, err = svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{WhiteLabel: &denied},
	}, "superadmin:alice", "")
	r.NoError(err)

	resolved, err = svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.WhiteLabel)
	r.False(*resolved.Limits.WhiteLabel)
}

// TestOrgAdminRowStillCountsAsPaid keeps the scheduling behavior a self-hosted
// operator already had: splitting `org-admin` out of `admin` is about who may
// outrank billing, not about who gets scheduling protection.
func TestOrgAdminRowStillCountsAsPaid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := saasSetup(t)
	ctx := t.Context()

	_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceOrgAdmin,
		Limits: entitlements.Limits{MaxChecks: entitlements.Int(500)},
	}, "user:bob", "")
	r.NoError(err)

	r.Equal(entitlements.PlanWeightPaid, svc.PlanWeight(ctx, org.UID),
		"an org-admin row is still a provisioned org")
}

// TestLegacyPartialRowResolvesToDefaultsAfterRelabel is the pair the migration
// exists for, asserted at the level an operator actually feels it.
//
// The shape is the real one: three caps stated and the rest absent, which is
// what every pre-spec write produced (the CLI coverage test writes exactly
// this). Post-migration that row is `org-admin`, so its unset caps still fall
// back to the deployment defaults.
//
// The positive control is the whole point: the SAME partial payload, written
// through the new superadmin editor as `admin`, resolves whole-row — unlimited.
// Without it, a migration that relabelled every row on the instance (or a
// resolver that had quietly lost whole-row mode) would sail through.
func TestLegacyPartialRowResolvesToDefaultsAfterRelabel(t *testing.T) {
	t.Parallel()

	// The partial payload both halves share.
	partial := func() entitlements.Limits {
		return entitlements.Limits{
			MaxChecks:          entitlements.Int(100),
			MaxUsers:           entitlements.Int(50),
			MaxChecksPerMinute: entitlements.Int(12),
		}
	}

	t.Run("a relabelled legacy row keeps the deployment defaults", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, _ := saasSetup(t)
		ctx := t.Context()

		_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
			Source: models.EntitlementSourceOrgAdmin,
			Limits: partial(),
		}, "user:bob", "")
		r.NoError(err)

		resolved, err := svc.Resolve(ctx, org.UID)
		r.NoError(err)

		// The caps the row never mentioned. Before the migration these would
		// have read as unlimited — unbounded messaging spend on the instance's
		// own credentials, and no SLO or agent ceiling at all.
		r.NotNil(resolved.Limits.MaxSlos)
		r.Equal(2, *resolved.Limits.MaxSlos)
		r.NotNil(resolved.Limits.MaxSmsPerMonth)
		r.Equal(0, *resolved.Limits.MaxSmsPerMonth)
		r.NotNil(resolved.Limits.MaxCallsPerMonth)
		r.Equal(0, *resolved.Limits.MaxCallsPerMonth)
		r.NotNil(resolved.Limits.MaxWhatsappPerMonth)
		r.Equal(0, *resolved.Limits.MaxWhatsappPerMonth)

		// And the caps it did state are honored, so the row is still doing its job.
		r.NotNil(resolved.Limits.MaxChecks)
		r.Equal(100, *resolved.Limits.MaxChecks)
		r.NotNil(resolved.Limits.MaxChecksPerMinute)
		r.Equal(12, *resolved.Limits.MaxChecksPerMinute)
	})

	t.Run("a superadmin override still resolves whole-row", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, _ := saasSetup(t)
		ctx := t.Context()

		_, err := svc.Apply(ctx, org.UID, entitlements.Entitlements{
			Source: models.EntitlementSourceAdmin,
			Limits: partial(),
		}, "superadmin:alice", "")
		r.NoError(err)

		resolved, err := svc.Resolve(ctx, org.UID)
		r.NoError(err)

		// Same omissions, opposite reading — because the editor saves a
		// COMPLETE row, so an absent cap there is a deliberate "unlimited".
		r.Nil(resolved.Limits.MaxSlos)
		r.Nil(resolved.Limits.MaxSmsPerMonth)
		r.Nil(resolved.Limits.MaxCallsPerMonth)
		r.Nil(resolved.Limits.MaxWhatsappPerMonth)

		r.NotNil(resolved.Limits.MaxChecks)
		r.Equal(100, *resolved.Limits.MaxChecks)
	})
}
