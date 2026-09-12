package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestImportDryRunRejectsWhatTheRealRunRejects is regression test 2 of spec
// 2026-09-10-01. The dry run for the real 47-check document returned
// `{"created": 47, "errors": []}` for a document that could not possibly
// succeed, because importSingleCheck returned as soon as it had decided
// created-vs-updated. A dry run that skips the validation the real path
// performs is worse than none.
func TestImportDryRunRejectsWhatTheRealRunRejects(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "dry-labels", nil)

	doc := importDocument(rig.org.Slug,
		httpCheck("dry-one", map[string]string{"os": "linux"}),
		httpCheck("dry-two", map[string]string{"k8s.cluster": "prod"}),
		httpCheck("dry-three", map[string]string{"environment": ""}),
	)

	dry := rig.importDoc(t, doc, true)

	r.True(dry.DryRun)
	r.Equal(0, dry.Created, "a document that cannot be written must not dry-run as created")
	r.Equal(0, dry.Updated)
	r.Len(dry.Errors, 3)
	r.Equal(0, rig.countChecks(t), "a dry run writes nothing")

	// And the real run reports EXACTLY the same thing, per item.
	applied := rig.importDoc(t, doc, false)

	r.False(applied.DryRun)
	r.Equal(dry.Created, applied.Created)
	r.Equal(dry.Updated, applied.Updated)
	r.Len(applied.Errors, len(dry.Errors))

	for i := range dry.Errors {
		r.Equal(dry.Errors[i].Slug, applied.Errors[i].Slug)
		r.Equal(dry.Errors[i].Error, applied.Errors[i].Error,
			"dry run and real run must produce the same per-item error string")
	}

	r.Equal(0, rig.countChecks(t))
}

// TestImportDryRunPositiveControls is the other half of regression test 2: a
// dry run that says "yes" has to be right about that too, or the fix would
// just be a validator that refuses everything.
func TestImportDryRunPositiveControls(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "dry-ok", nil)

	doc := importDocument(rig.org.Slug,
		httpCheck("ok-one", map[string]string{"environment": "prod"}),
		httpCheck("ok-two", map[string]string{"team": "platform"}),
	)

	dry := rig.importDoc(t, doc, true)
	r.Empty(dry.Errors, "a valid document must dry-run clean")
	r.Equal(2, dry.Created)
	r.Equal(0, dry.Updated)
	r.Equal(0, rig.countChecks(t))
	r.NotEmpty(dry.Caveats, "the dry run must name what it provably cannot check")

	// The real run produces the IDENTICAL counts the dry run promised.
	applied := rig.importDoc(t, doc, false)
	r.Empty(applied.Errors, "%+v", applied.Errors)
	r.Equal(dry.Created, applied.Created)
	r.Equal(dry.Updated, applied.Updated)
	r.Equal(2, rig.countChecks(t))

	// Re-running the same document changes nothing, and the dry run says so
	// before the real one does: `created=0 updated=0` with N unchanged is the
	// machine-readable "the file matches the instance" (spec 2026-09-11-04).
	dryAgain := rig.importDoc(t, doc, true)
	r.Equal(0, dryAgain.Created)
	r.Equal(0, dryAgain.Updated)
	r.Equal(2, dryAgain.Unchanged)
	r.Empty(dryAgain.Errors)
}

// TestImportDryRunCatchesInvalidConfig covers the checker's own Validate — the
// dry run used to stop before the UpsertCheckRequest was even built, so a
// config no checker would accept dry-ran clean.
func TestImportDryRunCatchesInvalidConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "dry-config", nil)

	bad := map[string]any{
		"name": "Broken", "slug": "broken-config", "type": "http", "enabled": true,
		"config": map[string]any{"url": "https://example.com/x", "method": "NOT-A-VERB"},
	}

	dry := rig.importDoc(t, importDocument(rig.org.Slug, bad), true)
	r.Equal(0, dry.Created)
	r.Len(dry.Errors, 1)
	r.Equal("broken-config", dry.Errors[0].Slug)

	applied := rig.importDoc(t, importDocument(rig.org.Slug, bad), false)
	r.Equal(0, applied.Created)
	r.Len(applied.Errors, 1)
	r.Equal(dry.Errors[0].Error, applied.Errors[0].Error)
	r.Equal(0, rig.countChecks(t))
}

// TestImportDryRunCatchesQuota covers the entitlement quota: an over-cap
// document must be refused by the dry run, not discovered halfway through the
// real one.
func TestImportDryRunCatchesQuota(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "dry-quota", nil)

	entSvc := entcore.NewService(rig.dbSvc, entcore.DefaultsFor("self-hosted"), 0)
	r.NoError(entSvc.Set(t.Context(), rig.org.UID, entcore.Entitlements{
		Limits: entcore.Limits{MaxChecks: entcore.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:test", ""))

	doc := importDocument(rig.org.Slug,
		httpCheck("quota-one", nil),
		httpCheck("quota-two", nil),
	)

	dry := rig.importDoc(t, doc, true)

	// The first fits under the cap of 1; the second does not, and the dry run
	// says so rather than reporting created: 2.
	r.Equal(1, dry.Created)
	r.Len(dry.Errors, 1)
	r.Equal("quota-two", dry.Errors[0].Slug)
	r.Equal(0, rig.countChecks(t))

	applied := rig.importDoc(t, doc, false)
	r.Equal(dry.Created, applied.Created)
	r.Len(applied.Errors, len(dry.Errors))
	r.Equal(1, rig.countChecks(t))
}

// secretCheck is a check entry whose config carries a credential the server
// encrypts at rest, so the stored row gets a private config side.
func secretCheck(slug, password string) map[string]any {
	return map[string]any{
		"name": "Acme " + slug, "slug": slug, "type": "http", "enabled": true,
		"config": map[string]any{
			"url":       "https://example.com/" + slug,
			"basicAuth": "alice:" + password,
		},
	}
}

// TestImportDryRunNamesTheSecretMergeCaveat pins the one gap the planner
// genuinely has: for a check that already holds encrypted or region-sealed
// config, the real update validates the MERGE of the document's config with
// the stored secrets, which a dry run cannot reproduce without decrypting a
// row it must not touch. The spec requires such a gap to be named in the
// RESPONSE rather than silently omitted.
func TestImportDryRunNamesTheSecretMergeCaveat(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	rig := newLabelRigWithCreds(t, "dry-caveat", nil, creds)

	// A create-only dry run carries the slug-race caveat and nothing else:
	// there is no stored row whose secrets could differ from the document.
	createOnly := importDocument(rig.org.Slug, secretCheck("caveat-one", "hunter2"))

	dry := rig.importDoc(t, createOnly, true)
	r.Equal([]checks.DryRunCaveat{checks.DryRunCaveatSlugRace}, dry.Caveats,
		"a create-only document has no stored secrets to merge")

	// Write it for real, so the next run is a would-update against a row with
	// an encrypted config side.
	applied := rig.importDoc(t, createOnly, false)
	r.Empty(applied.Errors, "%+v", applied.Errors)
	r.Equal(1, applied.Created)

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "caveat-one")
	r.NoError(err)
	r.NotNil(stored.ConfigPrivate, "the fixture must really hold an encrypted config side")

	// Now the same document is a would-write against an existing check, and
	// the caveat appears. The document carries the check's declared secret
	// inline, which a dry run cannot compare against the encrypted column — so
	// it is an update (a masked one), never a claimed "unchanged".
	dryUpdate := rig.importDoc(t, createOnly, true)
	r.Equal(0, dryUpdate.Created)
	r.Equal(1, dryUpdate.Updated)
	r.Contains(dryUpdate.Caveats, checks.DryRunCaveatSecretMerge)
	r.Contains(dryUpdate.Caveats, checks.DryRunCaveatSlugRace)

	// Negative control: a would-update against a check with NO stored secrets
	// has nothing the merge could change, so the caveat must not appear — a
	// caveat emitted unconditionally would tell the caller nothing.
	plain := importDocument(rig.org.Slug, httpCheck("caveat-plain", nil))

	applied = rig.importDoc(t, plain, false)
	r.Empty(applied.Errors, "%+v", applied.Errors)

	dryPlain := rig.importDoc(t, plain, true)
	r.Equal(0, dryPlain.Updated)
	r.Equal(1, dryPlain.Unchanged)
	r.NotContains(dryPlain.Caveats, checks.DryRunCaveatSecretMerge)
	r.Equal([]checks.DryRunCaveat{checks.DryRunCaveatSlugRace}, dryPlain.Caveats)

	// A real run never carries caveats at all — it is not planning anything.
	r.Empty(rig.importDoc(t, plain, false).Caveats)
}
