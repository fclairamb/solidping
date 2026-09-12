package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/orgparams"
)

// importOne wraps a single check in a v2 document for the org.
func importOne(org string, check checks.ExportCheck) *checks.ExportDocument {
	return &checks.ExportDocument{
		Version: 2, Organization: org, Secrets: checks.SecretsMarkerStripped,
		Checks: []checks.ExportCheck{check},
	}
}

// TestEntryNamingAUidIsCountedAsTheUpdateItIs guards the seam between the
// planner and the writer.
//
// The plan is computed from a snapshot keyed by SLUG, while the upsert resolves
// uid-or-slug (GetCheckByUidOrSlug) and nothing constrains a document entry's
// `slug` to be a slug. So an entry naming a UID matches a row the snapshot
// never indexed — and if the planner's "I have never seen this slug" were
// allowed to stand, the response would report `created: 1` for a row it
// UPDATED. That is the same counting lie this whole spec exists to remove, one
// layer down, and it would be invisible: the check really does get written, and
// only the number is wrong.
func TestEntryNamingAUidIsCountedAsTheUpdateItIs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Live", Slug: "live", Type: "http",
		Config: map[string]any{"url": "https://acme.com/live"},
	}))

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "live")
	r.NoError(err)

	byUID := importOne(rig.org.Slug, checks.ExportCheck{
		Name: "Live", Slug: stored.UID, Type: "http", Enabled: true,
		Config: map[string]any{"url": "https://acme.com/live"},
	})

	dry, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, byUID, true)
	r.NoError(err)
	r.Empty(dry.Errors, "%+v", dry.Errors)
	r.Equalf(0, dry.Created,
		"an entry naming an existing check's UID updates it, it does not create: %+v", dry.Plan)
	r.Equal(0, dry.Updated)
	r.Equal(1, dry.Unchanged, "the identifier resolves uid-or-slug, so the diff is real: %+v", dry.Plan)

	// The real run agrees with its own dry run, and really did not create a
	// second row — the count and the database say the same thing.
	applied, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, byUID, false)
	r.NoError(err)
	r.Empty(applied.Errors, "%+v", applied.Errors)
	r.Equal(0, applied.Created)
	r.Equal(1, applied.Unchanged)

	all, _, err := rig.dbSvc.ListChecks(t.Context(), rig.org.UID, nil)
	r.NoError(err)
	r.Len(all, 1, "the import must not have created a second check")

	// A UID-named entry that really does differ is an update naming the field
	// — the resolution is not a blanket "call everything unchanged".
	byUID.Checks[0].Config = map[string]any{"url": "https://acme.com/moved"}

	moved, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, byUID, true)
	r.NoError(err)
	r.Equal(0, moved.Created)
	r.Equal(1, moved.Updated)
	r.Equal("config.url", moved.Plan[0].Changes[0].Field)
}

// TestApplyDryRunResolvesTheSameIdentifierTheApplyDoes closes the second
// decision point.
//
// The apply DRY RUN computes its own plan, while the real apply routes through
// importChecks and therefore through resolveImportAction. Those two deciding
// create-vs-update differently is precisely the class of defect this spec
// exists to remove — a dry run that says one thing and a write that does
// another — so the plan resolves the identifier the way the upsert does, and
// this pins that the two answers match.
func TestApplyDryRunResolvesTheSameIdentifierTheApplyDoes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	// Applied (not merely created) so the check carries the managed label and
	// the plan can speak about ownership as well as content.
	seed := importOne(rig.org.Slug, checks.ExportCheck{
		Name: "Live", Slug: "live", Type: "http", Enabled: true,
		Config: map[string]any{"url": "https://acme.com/live"},
	})
	_, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, seed, checks.ApplyOptions{})
	r.NoError(err)

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "live")
	r.NoError(err)

	byUID := importOne(rig.org.Slug, checks.ExportCheck{
		Name: "Live", Slug: stored.UID, Type: "http", Enabled: true,
		Config: map[string]any{"url": "https://acme.com/live"},
	})

	dry, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, byUID, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equalf(0, dry.Created, "the dry run must not plan a create for a row the apply will update: %+v", dry.Plan)
	r.Equal(1, dry.Unchanged)

	real1, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, byUID, checks.ApplyOptions{})
	r.NoError(err)
	r.Empty(real1.Errors, "%+v", real1.Errors)
	r.Equalf(dry.Created, real1.Created, "the apply must agree with its own dry run")
	r.Equal(dry.Unchanged, real1.Unchanged)

	all, _, err := rig.dbSvc.ListChecks(t.Context(), rig.org.UID, nil)
	r.NoError(err)
	r.Len(all, 1)

	// And prune does not delete the check the manifest describes under its
	// UID: ownership is keyed on the ROW's slug, not on what the file called
	// it. Without that, the managed `live` would read as absent from the file.
	pruned, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, byUID,
		checks.ApplyOptions{DryRun: true, Prune: true})
	r.NoError(err)
	r.Equalf(0, pruned.Deleted, "a check named by UID is present in the file: %+v", pruned.Plan)
}

// TestReferenceDerivedValuesAreMaskedInTheDiff covers the second class the spec
// names as "must be masked", alongside declared secrets.
//
// A ${param:…} reference is stored VERBATIM since spec 2026-09-11-03, which is
// what makes it round-trip — and also what would put it in a field diff in
// clear if nothing masked it. The reference NAME is not itself a secret, but a
// plan is printed into CI logs and pasted into tickets next to real values, and
// "which parameter holds the production SSO password" is not a thing to publish
// by accident.
func TestReferenceDerivedValuesAreMaskedInTheDiff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	// Both references must RESOLVE: /import refuses a document carrying an
	// unresolvable one before it plans anything (spec 2026-09-11-03), so
	// without these the test would never reach the diff it is about.
	secret := true
	params := orgparams.NewService(rig.dbSvc)
	for _, key := range []string{"sso-current", "sso-rotated"} {
		_, err := params.Set(t.Context(), rig.org.Slug, key,
			&orgparams.SetRequest{Value: "hunter2", Secret: &secret})
		r.NoError(err)
	}

	// The reference lives in a PUBLIC key (`body`), not a declared secret —
	// otherwise the exporter would strip it and the masking under test would
	// never be reached. This is exactly the shape spec 2026-09-11-03 describes.
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "SSO", Slug: "sso", Type: "http",
		Config: map[string]any{
			"url":    "https://sso.acme.com/token",
			"method": "POST",
			"body":   "grant_type=password&password=${param:sso-current}",
		},
	}))

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(doc.Checks, 1)

	// Sanity: the reference really did survive into the document. If the
	// exporter had stripped it, everything below would pass vacuously.
	r.Contains(doc.Checks[0].Config["body"], "${param:sso-current}")

	doc.Checks[0].Config["body"] = "grant_type=password&password=${param:sso-rotated}"

	// Only the REFERENCE is masked, not the whole value: the surrounding
	// context is what tells the operator which field moved, and it carries no
	// secret. Both sides therefore render identically — which is the correct
	// reading of "a reference changed, and I will not tell you to what".
	const maskedBody = `"grant_type=password\u0026password=***"`

	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)
	r.Len(result.Plan, 1)
	r.Equal(checks.ActionUpdate, result.Plan[0].Action)
	r.Equal([]checks.CheckFieldChange{{Field: "config.body", From: maskedBody, To: maskedBody}},
		result.Plan[0].Changes,
		"a value carrying a ${param:…} reference must be masked on BOTH sides of the diff")

	// Positive control: the masking is keyed on the reference, not on the field
	// name — the same key with an ordinary value is printed IN FULL, so the
	// assertion above would fail if maskReferences stopped being applied rather
	// than passing because everything is masked.
	doc.Checks[0].Config["body"] = "grant_type=client_credentials"

	plain, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)
	r.Len(plain.Plan, 1)
	r.Len(plain.Plan[0].Changes, 1)
	r.Equal(maskedBody, plain.Plan[0].Changes[0].From, "the stored side still holds the reference")
	r.Equal(`"grant_type=client_credentials"`, plain.Plan[0].Changes[0].To,
		"an ordinary value must be shown, or the diff tells the operator nothing")
	r.NotContains(plain.Plan[0].Changes[0].From, "sso-current",
		"the parameter NAME must not survive the mask")
}

// TestEscalationThresholdIsReportedRatherThanSilentlyIgnored pins the honest
// half of a real gap.
//
// The exporter EMITS escalationThreshold, but no request struct carries it —
// not UpsertCheckRequest, not CreateCheckRequest, not UpdateCheckRequest — so
// editing it in a tracked file changes nothing anywhere. Excluding it from the
// diff made the plan answer `unchanged` for a file that genuinely differed:
// a false no-op, which is the same failure this spec exists to remove, pointing
// the other way. It is therefore reported as a change, with a warning naming
// the field and the slug, until the field becomes writable.
func TestEscalationThresholdIsReportedRatherThanSilentlyIgnored(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Live", Slug: "live", Type: "http",
		Config: map[string]any{"url": "https://acme.com/live"},
	}))

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.NotNil(doc.Checks[0].EscalationThreshold, "the exporter must emit the field this test is about")

	// A fresh export is still a no-op — the guarantee is not weakened by
	// diffing the field, only by pretending it always matches.
	clean, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)
	r.Equal(1, clean.Unchanged)
	r.Empty(clean.Warnings)

	edited := *doc.Checks[0].EscalationThreshold + 1
	doc.Checks[0].EscalationThreshold = &edited

	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)
	r.Equal(0, result.Unchanged, "a file that differs must never plan as unchanged")
	r.Equal(1, result.Updated)
	r.Len(result.Plan, 1)
	r.Contains(result.Plan[0].Changes,
		checks.CheckFieldChange{Field: "escalationThreshold", From: "10", To: "11"})

	// And the caller is told the plan cannot converge, rather than being left
	// to wonder why applying never clears the difference.
	r.Len(result.Warnings, 1)
	r.Contains(result.Warnings[0], "escalationThreshold")
	r.Contains(result.Warnings[0], "live")
	r.Contains(result.Warnings[0], "no write path can apply it")
}
