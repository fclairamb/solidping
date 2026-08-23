package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestTracerouteePolicySurvivesAnExportImportRoundTrip is the safety property
// of the path-trace opt-out (spec 2026-08-21-10).
//
// WHY THIS TEST EXISTS AT ALL: `off` and `inherit` are NOT interchangeable.
// `inherit` resolves to the organization default, which is ON. So a round trip
// that silently dropped an explicit `off` would turn a deliberate opt-out —
// set, typically, on a check probing somebody else's network — into an opt-in,
// with no diff anywhere for an operator to notice. That is the one direction
// this field must never fail in.
//
// All three states are exercised in the same document so the test cannot pass
// by returning a constant: `off` and `on` must each survive as themselves, and
// `inherit` must stay `inherit` rather than being coerced to either.
func TestTraceroutePolicySurvivesAnExportImportRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, srcOrg := setupApplyService(t, true)
	ctx := t.Context()

	mk := func(slug, policy string) checks.CreateCheckRequest {
		req := checks.CreateCheckRequest{
			Name: slug, Slug: slug, Type: "http",
			Config:  map[string]any{"url": "https://acme.com/" + slug},
			Regions: []string{"default"},
		}
		if policy != "" {
			req.TracerouteOnFailure = &policy
		}

		return req
	}

	_, err := svc.CreateCheck(ctx, srcOrg.Slug, mk("traced-off", checks.TraceroutePolicyOff))
	r.NoError(err)
	_, err = svc.CreateCheck(ctx, srcOrg.Slug, mk("traced-on", checks.TraceroutePolicyOn))
	r.NoError(err)
	_, err = svc.CreateCheck(ctx, srcOrg.Slug, mk("traced-inherit", checks.TraceroutePolicyInherit))
	r.NoError(err)

	// Positive control on the SOURCE: the three states really are distinct in
	// the database, so the assertions after the round trip mean something.
	srcOff, err := dbSvc.GetCheckByUidOrSlug(ctx, srcOrg.UID, "traced-off")
	r.NoError(err)
	r.NotNil(srcOff.TracerouteOnFailure)
	r.False(*srcOff.TracerouteOnFailure)

	srcInherit, err := dbSvc.GetCheckByUidOrSlug(ctx, srcOrg.UID, "traced-inherit")
	r.NoError(err)
	r.Nil(srcInherit.TracerouteOnFailure)

	exported, err := svc.ExportChecks(ctx, srcOrg.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	body, err := checks.MarshalExportDocument(exported)
	r.NoError(err)

	// The document itself must carry the decisions, and must NOT carry a line
	// for the state that means "no decision".
	raw := string(body)
	r.Contains(raw, `"tracerouteOnFailure": "off"`)
	r.Contains(raw, `"tracerouteOnFailure": "on"`)
	r.NotContains(raw, `"tracerouteOnFailure": "inherit"`,
		"inherit IS the absent state; writing it would add a no-op line to every check")

	cleanOrg := makeCleanOrg(ctx, t, dbSvc, "traceroute-clean-org")

	var reimport checks.ExportDocument
	r.NoError(json.Unmarshal(body, &reimport))

	result, err := svc.ImportChecks(ctx, cleanOrg.Slug, &reimport, false)
	r.NoError(err)
	r.Empty(result.Errors)
	r.Equal(3, result.Created)

	off, err := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, "traced-off")
	r.NoError(err)
	r.NotNil(off.TracerouteOnFailure, "an explicit opt-out must not decay to inherit")
	r.False(*off.TracerouteOnFailure)

	on, err := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, "traced-on")
	r.NoError(err)
	r.NotNil(on.TracerouteOnFailure)
	r.True(*on.TracerouteOnFailure)

	inherit, err := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, "traced-inherit")
	r.NoError(err)
	r.Nil(inherit.TracerouteOnFailure, "inherit must not be coerced into an explicit answer")

	// A re-export from the clean org must be byte-identical to the first, which
	// is what proves the field is stable under repeated apply.
	reexported, err := svc.ExportChecks(ctx, cleanOrg.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	normalizeForCompare(exported)
	normalizeForCompare(reexported)

	first, err := checks.MarshalExportDocument(exported)
	r.NoError(err)
	second, err := checks.MarshalExportDocument(reexported)
	r.NoError(err)

	r.Equal(string(first), string(second))
}

// TestReimportMovesAPolicyBack is the idempotence half: applying a manifest
// that says `inherit` must MOVE a check that currently says `off` back to
// inherit, not leave it where it was. "Absent means leave unchanged" would make
// the applied state depend on history, which is the opposite of applying.
func TestReimportMovesAPolicyBack(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	off := checks.TraceroutePolicyOff
	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "movable", Slug: "movable", Type: "http",
		Config:              map[string]any{"url": "https://acme.com/movable"},
		Regions:             []string{"default"},
		TracerouteOnFailure: &off,
	})
	r.NoError(err)

	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "movable")
	r.NoError(err)
	r.NotNil(stored.TracerouteOnFailure)

	// A document with no tracerouteOnFailure line at all — i.e. `inherit`.
	doc := &checks.ExportDocument{
		Version: 2,
		Checks: []checks.ExportCheck{{
			Name: "movable", Slug: "movable", Type: "http",
			Config:  map[string]any{"url": "https://acme.com/movable"},
			Regions: []string{"default"},
			Enabled: true,
		}},
	}

	result, err := svc.ImportChecks(ctx, org.Slug, doc, false)
	r.NoError(err)
	r.Empty(result.Errors)

	moved, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "movable")
	r.NoError(err)
	r.Nil(moved.TracerouteOnFailure,
		"a manifest that says inherit must move the check back, not leave the old override")
}

// TestClonePreservesTheTraceroutePolicy — same failure direction as the export
// gap, one endpoint over: a cloned check must not silently start tracing
// because its source's opt-out was dropped.
func TestClonePreservesTheTraceroutePolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	off := checks.TraceroutePolicyOff
	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "sensitive", Slug: "sensitive", Type: "http",
		Config:              map[string]any{"url": "https://acme.com/sensitive"},
		Regions:             []string{"default"},
		TracerouteOnFailure: &off,
	})
	r.NoError(err)

	clone, err := svc.CloneCheck(ctx, org.Slug, created.UID, &checks.CloneCheckRequest{})
	r.NoError(err)

	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, clone.UID)
	r.NoError(err)
	r.NotNil(stored.TracerouteOnFailure, "a clone must inherit the source's explicit opt-out")
	r.False(*stored.TracerouteOnFailure)
}
