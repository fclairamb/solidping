package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
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
	real := rig.importDoc(t, doc, false)

	r.False(real.DryRun)
	r.Equal(dry.Created, real.Created)
	r.Equal(dry.Updated, real.Updated)
	r.Len(real.Errors, len(dry.Errors))

	for i := range dry.Errors {
		r.Equal(dry.Errors[i].Slug, real.Errors[i].Slug)
		r.Equal(dry.Errors[i].Error, real.Errors[i].Error,
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
	real := rig.importDoc(t, doc, false)
	r.Empty(real.Errors, "%+v", real.Errors)
	r.Equal(dry.Created, real.Created)
	r.Equal(dry.Updated, real.Updated)
	r.Equal(2, rig.countChecks(t))

	// Re-running the same document is now an update, and the dry run says so
	// before the real one does.
	dryAgain := rig.importDoc(t, doc, true)
	r.Equal(0, dryAgain.Created)
	r.Equal(2, dryAgain.Updated)
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

	real := rig.importDoc(t, importDocument(rig.org.Slug, bad), false)
	r.Equal(0, real.Created)
	r.Len(real.Errors, 1)
	r.Equal(dry.Errors[0].Error, real.Errors[0].Error)
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

	real := rig.importDoc(t, doc, false)
	r.Equal(dry.Created, real.Created)
	r.Len(real.Errors, len(dry.Errors))
	r.Equal(1, rig.countChecks(t))
}
