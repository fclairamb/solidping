package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portEntitlementSourceSplitMigration is distinct from every other
// _postgres_test.go file's port so the embedded servers never collide.
const portEntitlementSourceSplitMigration = 15492

// TestEntitlementSourceSplitMigrationParity_Postgres is the dialect-parity
// half of the entitlement source split (spec 2026-08-26-06). The behavioral
// twin runs against SQLite in
// internal/db/sqlite/entitlement_source_split_migration_test.go.
//
// The two dialects must agree on BEHAVIOR, not merely both apply: a relabel
// that matched a different set of rows on one engine would mean SaaS and
// self-hosted quietly resolve entitlements differently. Pure text assertions
// on the shipped files, so this runs everywhere rather than only where an
// embedded Postgres can start.
func TestEntitlementSourceSplitMigrationParity_Postgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	up := migrationSection(t, "entitlement-source-split")

	// The audit insert runs FIRST. Order is load-bearing: after the update, the
	// predicate matches nothing and the before-snapshot would be the post-
	// relabel payload, i.e. an audit trail that records no change.
	auditAt := strings.Index(up, "insert into org_entitlement_audits")
	updateAt := strings.Index(up, "update org_entitlements")
	r.GreaterOrEqual(auditAt, 0, "the relabel must be audited")
	r.Greater(updateAt, auditAt, "the audit must be captured before the payload moves")

	// Scoped to legacy admin rows and nothing else. Losing this predicate would
	// hand billing's own rows to the org-admin branch.
	r.Contains(up, `where payload->>'source' = 'admin'`)
	r.Contains(up, `jsonb_set(payload, '{source}', '"org-admin"')`)

	// Not scoped to one org, and never touching the audit table's own history.
	r.NotContains(up, "organization_uid =")
	r.NotContains(up, "update org_entitlement_audits")

	// The teardown maps org-admin back, which is what a downgraded binary
	// understands as "paid, null-filled, billing may overwrite".
	down := findMigrationSection(t, "down", "entitlement-source-split")
	r.Contains(down, `where payload->>'source' = 'org-admin'`)
	r.Contains(down, `jsonb_set(payload, '{source}', '"admin"')`)

	// v0.18.3 is released, so this cycle opens a NEW consolidated migration per
	// dialect (wiki/conventions/database.md): the section belongs in 016.
	body, err := migrationsFS.ReadFile("migrations/016_v0_19_0.up.sql")
	r.NoError(err)
	r.Contains(string(body), "-- SECTION: entitlement-source-split\n")
}

// TestEntitlementSourceSplitMigrationRelabels_Postgres EXECUTES the section
// against a real Postgres, so the parity test above is not the only thing
// standing between a typo and every legacy admin row silently becoming
// unlimited on SaaS.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestEntitlementSourceSplitMigrationRelabels_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{
		Embedded: true, Port: portEntitlementSourceSplitMigration, RunMode: runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	seed := func(slug, payload string) *models.Organization {
		org := models.NewOrganization(slug, slug)
		r.NoError(svc.CreateOrganization(ctx, org))
		_, execErr := svc.DB().ExecContext(ctx,
			`insert into org_entitlements (uid, organization_uid, payload, metadata)
			 values (gen_random_uuid(), ?, ?::jsonb, '{}'::jsonb)`,
			org.UID, payload)
		r.NoError(execErr)

		return org
	}

	// The PARTIAL admin row this migration exists for — three caps stated, the
	// rest absent, exactly what the org-scoped API and CLI have always written.
	legacy := seed("acmepgent",
		`{"version":1,"source":"admin","limits":{"maxChecks":100,"maxUsers":50,"maxChecksPerMinute":12}}`)
	// Negative control: billing's own row must not be relabelled.
	billed := seed("acmepgbill", `{"version":1,"source":"billing-service","limits":{"maxChecks":5000}}`)

	for _, stmt := range pgBunSplitRE.Split(migrationSection(t, "entitlement-source-split"), -1) {
		if !hasSQL(stmt) {
			continue
		}

		_, execErr := svc.DB().ExecContext(ctx, stmt)
		r.NoError(execErr, "statement failed:\n%s", stmt)
	}

	sourceOf := func(orgUID string) string {
		var source string
		r.NoError(svc.DB().QueryRowContext(ctx,
			`select payload->>'source' from org_entitlements where organization_uid = ?`,
			orgUID).Scan(&source))

		return source
	}

	r.Equal("org-admin", sourceOf(legacy.UID))
	r.Equal("billing-service", sourceOf(billed.UID))

	// Limits untouched: a relabel, not a rewrite.
	var maxChecks int
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select (payload->'limits'->>'maxChecks')::int from org_entitlements where organization_uid = ?`,
		legacy.UID).Scan(&maxChecks))
	r.Equal(100, maxChecks)

	audits, err := svc.ListOrgEntitlementAudits(ctx, models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: legacy.UID,
	})
	r.NoError(err)
	r.Len(audits, 1)
	r.Equal("migration:org-admin-relabel", audits[0].Source)
	r.Equal("admin", audits[0].BeforeSnapshot["source"])
	r.Equal("org-admin", audits[0].AfterSnapshot["source"])

	untouched, err := svc.ListOrgEntitlementAudits(ctx, models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: billed.UID,
	})
	r.NoError(err)
	r.Empty(untouched)
}
