package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// seedLegacyEntitlements inserts an org_entitlements row with a raw payload,
// bypassing the Go model on purpose: the rows this migration exists for were
// written by an older binary, and letting today's constructor stamp them would
// test the constructor rather than the migration.
func seedLegacyEntitlements(t *testing.T, svc *Service, orgUID, payload string) {
	t.Helper()

	_, err := svc.DB().ExecContext(t.Context(),
		`insert into org_entitlements (uid, organization_uid, payload, metadata, created_at, updated_at)
		 values (?, ?, ?, '{}', datetime('now'), datetime('now'))`,
		orgUID+"-ent", orgUID, payload)
	require.NoError(t, err)
}

func entitlementsSource(t *testing.T, svc *Service, orgUID string) string {
	t.Helper()

	var source string
	err := svc.DB().QueryRowContext(t.Context(),
		`select json_extract(payload, '$.source') from org_entitlements where organization_uid = ?`,
		orgUID).Scan(&source)
	require.NoError(t, err)

	return source
}

// TestEntitlementSourceSplitMigrationRelabelsLegacyAdminRows executes the
// `entitlement-source-split` section of the v0.19.0 migration.
//
// Spec 2026-08-26-06 gave `source='admin'` two powers it lacked when these rows
// were written: it suppresses billing pushes, and it resolves WHOLE-ROW, so an
// unset cap means UNLIMITED rather than "fall back to the deployment default".
// Every pre-existing 'admin' row came from the org-scoped door (until that spec
// landed, every non-service write got 'admin') and is routinely PARTIAL — so
// without this relabel, upgrading would silently uncap them.
//
// Both negative controls are asserted, because an `update ... where` that lost
// its predicate would satisfy the positive assertion perfectly while quietly
// rewriting billing's rows too.
func TestEntitlementSourceSplitMigrationRelabelsLegacyAdminRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	// 1. The row this migration exists for: a PARTIAL admin row, exactly the
	//    shape the CLI and the org-scoped API have always produced — three caps
	//    stated, everything else absent.
	legacy := models.NewOrganization("acme", "Acme")
	r.NoError(svc.CreateOrganization(ctx, legacy))
	seedLegacyEntitlements(t, svc, legacy.UID,
		`{"version":1,"source":"admin","limits":{"maxChecks":100,"maxUsers":50,"maxChecksPerMinute":12}}`)

	// 2. Negative control: a billing-service row. Relabelling it would hand the
	//    billing service's own rows to the org-admin branch.
	billed := models.NewOrganization("acmetech", "Acme Tech")
	r.NoError(svc.CreateOrganization(ctx, billed))
	seedLegacyEntitlements(t, svc, billed.UID,
		`{"version":1,"source":"billing-service","limits":{"maxChecks":5000}}`)

	// 3. Negative control: a self-hosted row, the startup-hook shape.
	local := models.NewOrganization("acmelabs", "Acme Labs")
	r.NoError(svc.CreateOrganization(ctx, local))
	seedLegacyEntitlements(t, svc, local.UID,
		`{"version":1,"source":"self-hosted","limits":{}}`)

	execMigrationSection(ctx, t, svc, migrationSection(t, "entitlement-source-split"))

	r.Equal("org-admin", entitlementsSource(t, svc, legacy.UID),
		"a legacy admin row must lose the powers it was never granted")
	r.Equal("billing-service", entitlementsSource(t, svc, billed.UID))
	r.Equal("self-hosted", entitlementsSource(t, svc, local.UID))

	// The limits themselves are untouched — this is a relabel, not a rewrite.
	var maxChecks int
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select json_extract(payload, '$.limits.maxChecks') from org_entitlements where organization_uid = ?`,
		legacy.UID).Scan(&maxChecks))
	r.Equal(100, maxChecks)

	// An audit row explains the relabel to whoever opens the org next. Written
	// only for the row that actually moved.
	audits, err := svc.ListOrgEntitlementAudits(ctx, models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: legacy.UID,
	})
	r.NoError(err)
	r.Len(audits, 1)
	r.Equal("migration:org-admin-relabel", audits[0].Source)
	r.NotNil(audits[0].Reason)
	r.Contains(*audits[0].Reason, "org-admin")
	// before/after must straddle the change, so the trail reads honestly.
	r.Equal("admin", audits[0].BeforeSnapshot["source"])
	r.Equal("org-admin", audits[0].AfterSnapshot["source"])

	for _, org := range []*models.Organization{billed, local} {
		untouched, auditErr := svc.ListOrgEntitlementAudits(ctx, models.ListOrgEntitlementAuditsFilter{
			OrganizationUID: org.UID,
		})
		r.NoError(auditErr)
		r.Empty(untouched, "a row the migration did not move must not be audited as if it had")
	}

	// Re-running is a no-op: nothing matches the predicate any more. Migrations
	// should not be re-run, but a partially-applied one being retried must not
	// double-audit.
	execMigrationSection(ctx, t, svc, migrationSection(t, "entitlement-source-split"))

	audits, err = svc.ListOrgEntitlementAudits(ctx, models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: legacy.UID,
	})
	r.NoError(err)
	r.Len(audits, 1, "the relabel is idempotent")
}
