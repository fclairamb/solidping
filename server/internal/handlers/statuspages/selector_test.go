package statuspages

// Tests for dynamic status page sections (spec 2026-08-29-11).
//
// The feature exists to remove a SILENT failure — a new check that nobody
// remembered to attach, on a board that therefore lies green — so these tests
// are mostly about proving negatives: that a check which should NOT be there
// isn't, that a reconcile which should change NOTHING changes nothing, and
// that a manual row is never touched.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// --- helpers ---

// seedLabeledCheck creates an http check with the given DISPLAY NAME and
// labels. The name matters: it is the key the reconciler alphabetizes managed
// rows by, so a test that cares about order has to set it (models.NewCheck's
// second argument is the slug, not the name).
func seedLabeledCheck(
	ctx context.Context, t *testing.T, svc *Service, orgUID, name string, labels map[string]string,
) *models.Check {
	t.Helper()

	r := require.New(t)

	check := models.NewCheck(orgUID, "check-"+uuid.NewString()[:8], "http")
	check.Name = &name
	r.NoError(svc.db.CreateCheck(ctx, check))
	setCheckLabels(ctx, t, svc, orgUID, check.UID, labels)

	return check
}

// setCheckLabels replaces a check's labels, exactly as the checks service does
// (GetOrCreateLabel + SetCheckLabels, replace-all semantics).
func setCheckLabels(
	ctx context.Context, t *testing.T, svc *Service, orgUID, checkUID string, labels map[string]string,
) {
	t.Helper()

	r := require.New(t)

	uids := make([]string, 0, len(labels))

	for key, value := range labels {
		label, err := svc.db.GetOrCreateLabel(ctx, orgUID, key, value)
		r.NoError(err)

		uids = append(uids, label.UID)
	}

	r.NoError(svc.db.SetCheckLabels(ctx, checkUID, uids))
}

// sectionCheckUIDs returns the section's resources as (checkUID, managed)
// pairs, in stored position order — the exact order the public page renders.
func sectionCheckUIDs(
	ctx context.Context, t *testing.T, svc *Service, sectionUID string,
) ([]string, []bool) {
	t.Helper()

	resources, err := svc.db.ListStatusPageResources(ctx, sectionUID)
	require.NoError(t, err)

	uids := make([]string, len(resources))
	managed := make([]bool, len(resources))

	for i, resource := range resources {
		uids[i] = *resource.CheckUID
		managed[i] = resource.ManagedBySelector
	}

	return uids, managed
}

// selectorRaw renders a selector as the raw JSON a request body would carry.
func selectorRaw(t *testing.T, selector any) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(selector)
	require.NoError(t, err)

	return raw
}

// seedSelectorPage creates a page with a single section carrying the selector,
// with the seeded default "Services" section removed so the assertions are
// about exactly one section.
func seedSelectorPage(
	ctx context.Context, t *testing.T, svc *Service, org *models.Organization, selector any,
) (StatusPageResponse, StatusPageSectionResponse) {
	t.Helper()

	r := require.New(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)

	dropDefaultSections(ctx, t, svc, page.UID)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{
		Name: "Everything", Slug: "everything", Selector: selectorRaw(t, selector),
	})
	r.NoError(err)

	return page, section
}

// --- Selector validation ---

// TestSectionSelector_Validate pins every way a selector can be rejected.
//
// Rejection matters more than acceptance here: a selector that is *almost*
// right — an empty labels map, a bare `{}` — would otherwise be a rule that
// silently matches everything or nothing, forever, which is the failure this
// whole feature is meant to delete.
func TestSectionSelector_Validate(t *testing.T) {
	t.Parallel()

	longValue := make([]byte, models.SectionSelectorMaxValueLen+1)
	for i := range longValue {
		longValue[i] = 'a'
	}

	tooMany := make(map[string]string, models.SectionSelectorMaxLabels+1)
	for i := 0; i <= models.SectionSelectorMaxLabels; i++ {
		tooMany["key"+string(rune('a'+i))] = "v"
	}

	tests := []struct {
		name     string
		selector *models.SectionSelector
		wantErr  error
	}{
		{"all", &models.SectionSelector{All: true}, nil},
		{"labels", &models.SectionSelector{Labels: map[string]string{"env": "prod"}}, nil},
		{"nil", nil, models.ErrSelectorEmpty},
		{"empty object", &models.SectionSelector{}, models.ErrSelectorEmpty},
		{
			"all and labels",
			&models.SectionSelector{All: true, Labels: map[string]string{"env": "prod"}},
			models.ErrSelectorAmbiguous,
		},
		{
			"empty labels map",
			&models.SectionSelector{Labels: map[string]string{}},
			models.ErrSelectorLabelsEmpty,
		},
		{"too many labels", &models.SectionSelector{Labels: tooMany}, models.ErrSelectorTooManyLabels},
		{
			"bad key",
			&models.SectionSelector{Labels: map[string]string{"E n v": "prod"}},
			models.ErrSelectorLabelKeyInvalid,
		},
		{
			"empty value",
			&models.SectionSelector{Labels: map[string]string{"env": ""}},
			models.ErrSelectorLabelValueInvalid,
		},
		{
			"over-long value",
			&models.SectionSelector{Labels: map[string]string{"env": string(longValue)}},
			models.ErrSelectorLabelValueInvalid,
		},
		{
			// v1 has no existence-only matching: "*" is an exact value, and a
			// user expecting a wildcard gets an empty section rather than a
			// silent everything-match.
			"star is a literal value",
			&models.SectionSelector{Labels: map[string]string{"public": "*"}},
			nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := test.selector.Validate()
			if test.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

// TestParseSelector_Strict pins that a mistyped key is a hard error rather
// than a rule that quietly matches nothing.
func TestParseSelector_Strict(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Absent and explicit-null both mean "no selector" — the difference
	// between them lives in the request struct, not here.
	selector, err := parseSelector(nil)
	r.NoError(err)
	r.Nil(selector)

	selector, err = parseSelector(json.RawMessage(`null`))
	r.NoError(err)
	r.Nil(selector)

	// The typo case. `lables` is not `labels`.
	_, err = parseSelector(json.RawMessage(`{"lables":{"env":"prod"}}`))
	r.ErrorIs(err, ErrSelectorInvalid)
	r.True(selectorValidationError(err))

	_, err = parseSelector(json.RawMessage(`{`))
	r.ErrorIs(err, ErrSelectorInvalid)

	selector, err = parseSelector(json.RawMessage(`{"all":true}`))
	r.NoError(err)
	r.True(selector.All)
}

// TestCreateSection_RejectsInvalidSelector pins that the validation reaches
// the API boundary as a VALIDATION_ERROR-shaped failure rather than a 500.
func TestCreateSection_RejectsInvalidSelector(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)

	for _, raw := range []string{
		`{}`,
		`{"labels":{}}`,
		`{"all":true,"labels":{"env":"prod"}}`,
		`{"lables":{"env":"prod"}}`,
	} {
		_, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{
			Name: "Dyn", Slug: "dyn", Selector: json.RawMessage(raw),
		})
		r.Error(err, raw)
		r.True(selectorValidationError(err), raw)
	}
}

// --- Reconciliation behavior ---

// TestSelector_LabelRoundTrip is the headline requirement: a check created
// AFTER the page was built, carrying the right label, appears with no manual
// action — and drops off the moment the label is removed.
func TestSelector_LabelRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	_, section := seedSelectorPage(ctx, t, svc, org,
		models.SectionSelector{Labels: map[string]string{"public": "true"}})

	// Nothing labeled yet.
	uids, _ := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Empty(uids)

	// A brand-new labeled check — created long after the page.
	check := seedLabeledCheck(ctx, t, svc, org.UID, "API", map[string]string{"public": "true"})
	svc.ReconcileOrgSelectors(ctx, org.UID)

	uids, managed := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Equal([]string{check.UID}, uids)
	r.Equal([]bool{true}, managed)

	// Removing the label takes it off again.
	setCheckLabels(ctx, t, svc, org.UID, check.UID, nil)
	svc.ReconcileOrgSelectors(ctx, org.UID)

	uids, _ = sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Empty(uids)
}

// TestSelector_AndSemantics pins that a multi-pair selector requires EVERY
// pair. A check matching one of two labels must not appear — the negative is
// the whole point, since an OR would silently over-publish.
func TestSelector_AndSemantics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, section := seedSelectorPage(ctx, t, svc, org,
		models.SectionSelector{Labels: map[string]string{"env": "prod", "public": "true"}})

	both := seedLabeledCheck(ctx, t, svc, org.UID, "Both",
		map[string]string{"env": "prod", "public": "true"})
	seedLabeledCheck(ctx, t, svc, org.UID, "Env only", map[string]string{"env": "prod"})
	seedLabeledCheck(ctx, t, svc, org.UID, "Public only", map[string]string{"public": "true"})
	seedLabeledCheck(ctx, t, svc, org.UID, "Wrong value",
		map[string]string{"env": "staging", "public": "true"})

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	uids, _ := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Equal([]string{both.UID}, uids)
}

// TestSelector_AllPicksUpUnlabeledCheckAndSkipsInternal pins two things about
// `{"all":true}`: it adopts a brand-new check that carries no labels at all,
// and it never adopts an INTERNAL check. The second is a disclosure guarantee
// — internal probes are the org's own plumbing and must not reach a page.
func TestSelector_AllPicksUpUnlabeledCheckAndSkipsInternal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	plain := models.NewCheck(org.UID, "plain", "http")
	plain.Name = strPtr("Plain")
	r.NoError(svc.db.CreateCheck(ctx, plain))

	internal := models.NewCheck(org.UID, "internal-probe", "http")
	internal.Name = strPtr("Internal probe")
	internal.Internal = true
	r.NoError(svc.db.CreateCheck(ctx, internal))

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	uids, _ := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Equal([]string{plain.UID}, uids)
}

// TestSelector_ManualWinsAndReadoption pins the dedupe rule in both
// directions: a manually placed check is never duplicated by a selector
// ANYWHERE on the page, and deleting that manual row hands the check back to
// the selector on the next reconcile.
func TestSelector_ManualWinsAndReadoption(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)
	dropDefaultSections(ctx, t, svc, page.UID)

	curated, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	dynamic, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{
		Name: "Everything", Slug: "everything",
		Selector: selectorRaw(t, models.SectionSelector{All: true}),
	})
	r.NoError(err)

	pinned := seedLabeledCheck(ctx, t, svc, org.UID, "Pinned", nil)
	other := seedLabeledCheck(ctx, t, svc, org.UID, "Other", nil)

	// Place `pinned` by hand in the CURATED section — a different section from
	// the dynamic one, because the rule is page-wide, not section-local.
	manual, err := svc.CreateResource(ctx, org.Slug, page.UID, curated.UID,
		CreateResourceRequest{CheckUID: pinned.UID})
	r.NoError(err)

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	curatedUIDs, curatedManaged := sectionCheckUIDs(ctx, t, svc, curated.UID)
	r.Equal([]string{pinned.UID}, curatedUIDs)
	r.Equal([]bool{false}, curatedManaged, "the manual row must stay manual")

	dynamicUIDs, _ := sectionCheckUIDs(ctx, t, svc, dynamic.UID)
	r.Equal([]string{other.UID}, dynamicUIDs, "the selector must skip the manually placed check")

	// Deleting the manual row releases the check back to the selector.
	r.NoError(svc.DeleteResource(ctx, org.Slug, page.UID, curated.UID, manual.UID))

	dynamicUIDs, dynamicManaged := sectionCheckUIDs(ctx, t, svc, dynamic.UID)
	sort.Strings(dynamicUIDs)
	expected := []string{pinned.UID, other.UID}
	sort.Strings(expected)
	r.Equal(expected, dynamicUIDs)
	r.Equal([]bool{true, true}, dynamicManaged)
}

// TestSelector_OrderingAndIdempotence pins the two properties status0 depends
// on: manual rows first in their own order, managed rows after in alphabetical
// order — and a second reconcile of an unchanged page issues NO writes, so two
// polls of a quiet page can never shuffle rows.
func TestSelector_OrderingAndIdempotence(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)
	dropDefaultSections(ctx, t, svc, page.UID)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{
		Name: "Mixed", Slug: "mixed",
		Selector: selectorRaw(t, models.SectionSelector{Labels: map[string]string{"public": "true"}}),
	})
	r.NoError(err)

	// Two manual rows, deliberately added in a non-alphabetical order: their
	// explicit placement must survive, unsorted.
	zulu := seedLabeledCheck(ctx, t, svc, org.UID, "Zulu manual", nil)
	alpha := seedLabeledCheck(ctx, t, svc, org.UID, "Alpha manual", nil)

	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: zulu.UID})
	r.NoError(err)
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: alpha.UID})
	r.NoError(err)

	// Three matching checks, created out of alphabetical order.
	charlie := seedLabeledCheck(ctx, t, svc, org.UID, "charlie", map[string]string{"public": "true"})
	bravo := seedLabeledCheck(ctx, t, svc, org.UID, "Bravo", map[string]string{"public": "true"})
	delta := seedLabeledCheck(ctx, t, svc, org.UID, "delta", map[string]string{"public": "true"})

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	uids, managed := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Equal([]string{zulu.UID, alpha.UID, bravo.UID, charlie.UID, delta.UID}, uids)
	r.Equal([]bool{false, false, true, true, true}, managed)

	// Snapshot every row's identity and updated_at, then reconcile again.
	before, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)

	snapshot := make(map[string]time.Time, len(before))
	for _, resource := range before {
		snapshot[resource.UID] = resource.UpdatedAt
	}

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	after, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(after, len(before))

	for i, resource := range after {
		r.Equal(before[i].UID, resource.UID, "row identity must not change")
		r.Equal(before[i].Position, resource.Position, "row order must not change")
		r.True(snapshot[resource.UID].Equal(resource.UpdatedAt),
			"an unchanged reconcile must not write the row at all")
	}
}

// TestSelector_OverlappingSectionsDoNotDuplicate pins that two selectors
// matching the same check produce ONE component, claimed by the earlier
// section — a page that lists the same service twice reads as a bug.
func TestSelector_OverlappingSectionsDoNotDuplicate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)
	dropDefaultSections(ctx, t, svc, page.UID)

	first, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{
		Name: "Prod", Slug: "prod",
		Selector: selectorRaw(t, models.SectionSelector{Labels: map[string]string{"env": "prod"}}),
	})
	r.NoError(err)

	second, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{
		Name: "All", Slug: "all",
		Selector: selectorRaw(t, models.SectionSelector{All: true}),
	})
	r.NoError(err)

	prod := seedLabeledCheck(ctx, t, svc, org.UID, "Prod api", map[string]string{"env": "prod"})

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	firstUIDs, _ := sectionCheckUIDs(ctx, t, svc, first.UID)
	secondUIDs, _ := sectionCheckUIDs(ctx, t, svc, second.UID)
	r.Equal([]string{prod.UID}, firstUIDs)
	r.Empty(secondUIDs, "the later section must not repeat a check the earlier one claimed")
}

// TestSelector_ClearingSelectorRemovesManagedRows pins that turning a dynamic
// section back into a hand-curated one takes its materialized rows with it —
// otherwise the page keeps advertising checks under a rule that no longer
// exists.
func TestSelector_ClearingSelectorRemovesManagedRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	seedLabeledCheck(ctx, t, svc, org.UID, "API", nil)
	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	uids, _ := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Len(uids, 1)

	updated, err := svc.UpdateSection(ctx, org.Slug, page.UID, section.UID, UpdateSectionRequest{
		Selector: json.RawMessage(`null`),
	})
	r.NoError(err)
	r.Nil(updated.Selector)

	uids, _ = sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Empty(uids)
}

// TestSelector_ManagedRowIsNotDeletable pins that the selector owns its rows:
// deleting one by hand is refused rather than accepted-then-silently-undone on
// the next reconcile.
func TestSelector_ManagedRowIsNotDeletable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	seedLabeledCheck(ctx, t, svc, org.UID, "API", nil)
	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	resources, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(resources, 1)

	err = svc.DeleteResource(ctx, org.Slug, page.UID, section.UID, resources[0].UID)
	r.ErrorIs(err, ErrResourceManagedBySelector)

	// Still there.
	uids, _ := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Len(uids, 1)
}

// TestSelector_PublicPayloadHidesSelector pins that the public page never
// spells out the org's label taxonomy, while the authenticated view does.
func TestSelector_PublicPayloadHidesSelector(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, _ := seedSelectorPage(ctx, t, svc, org,
		models.SectionSelector{Labels: map[string]string{"env": "prod"}})

	public, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.Len(public.Sections, 1)
	r.Nil(public.Sections[0].Selector)
	r.NotContains(mustMarshalJSON(t, public), "\"selector\"")
	r.NotContains(mustMarshalJSON(t, public), "env")
	// Nor does it say HOW a component got there — a materialized row renders
	// exactly like a manual one.
	r.NotContains(mustMarshalJSON(t, public), "managedBySelector")

	admin, err := svc.GetStatusPage(ctx, org.Slug, page.UID, GetStatusPageOptions{IncludeSections: true})
	r.NoError(err)
	r.Len(admin.Sections, 1)
	r.NotNil(admin.Sections[0].Selector)
	r.Equal(map[string]string{"env": "prod"}, admin.Sections[0].Selector.Labels)
}

// TestSelector_PublicViewIsABackstop pins the safety net: even with every
// write-path trigger bypassed (the checks are inserted straight into the
// database here), viewing the page materializes the missing rows.
func TestSelector_PublicViewIsABackstop(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	_, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	// Straight to the DB: no service call, so no reconcile trigger fires.
	check := models.NewCheck(org.UID, "drifted", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))

	uids, _ := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Empty(uids, "precondition: the drift exists")

	// The backstop marker was invalidated by CreateSection's own reconcile, so
	// this view is due.
	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.Len(view.Sections, 1)

	uids, managed := sectionCheckUIDs(ctx, t, svc, section.UID)
	r.Equal([]string{check.UID}, uids)
	r.Equal([]bool{true}, managed)
}

// TestSelectorReconcileMarks_RateLimits pins that the backstop is throttled:
// a hot public page must not run a reconcile on every single request.
func TestSelectorReconcileMarks_RateLimits(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	marks := &selectorReconcileMarks{}
	start := time.Now()

	r.True(marks.due("page", start), "first view is always due")
	r.False(marks.due("page", start.Add(time.Second)), "a second view within the interval is not")
	r.True(marks.due("page", start.Add(selectorBackstopInterval+time.Second)))

	marks.invalidate("page")
	r.True(marks.due("page", start.Add(selectorBackstopInterval+time.Second)),
		"an explicit invalidation forces the next view to reconcile")
}

// TestSelector_MatchTotalIsAdminOnly pins the counters the dashboard uses to
// say "showing N of M" — and that they never reach the public payload.
func TestSelector_MatchTotalIsAdminOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, _ := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	for _, name := range []string{"one", "two", "three"} {
		seedLabeledCheck(ctx, t, svc, org.UID, name, nil)
	}

	sections, err := svc.ListSections(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Len(sections, 1)
	r.Equal(3, sections[0].SelectorMatchTotal)
	r.False(sections[0].SelectorTruncated, "three checks are well under the cap")

	public, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.Zero(public.Sections[0].SelectorMatchTotal)
}

// TestSelector_ManagedRowsRenderLikeManualOnes pins spec §5: status0 needs no
// changes because a materialized row carries the same live check data a manual
// one does.
func TestSelector_ManagedRowsRenderLikeManualOnes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	_, _ = seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	check := models.NewCheck(org.UID, "checkout", "http")
	check.Name = strPtr("Checkout")
	check.Status = models.CheckStatusDown
	r.NoError(svc.db.CreateCheck(ctx, check))

	svc.ReconcileOrgSelectors(ctx, org.UID)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.Len(view.Sections, 1)
	r.Len(view.Sections[0].Resources, 1)

	resource := view.Sections[0].Resources[0]
	r.NotNil(resource.Check)
	r.Equal("Checkout", *resource.Check.Name)
	r.Equal("http", resource.Check.Type)
	r.False(resource.ManagedBySelector, "the public payload hides how a row got there")
	r.Equal(string(models.PageStatusDown), view.OverallStatus)
}

// TestSelector_CapsManagedRowsPerSection pins the blast-radius limit.
//
// It is not a micro-optimisation: a public page carries a full availability
// series per resource, measured at ~8 KB each on the default 90-day window, so
// 150 rows is already a ~1.2 MB payload. `{"all":true}` in a large org would
// otherwise put every check on one page. The cap keeps the payload bounded,
// takes a STABLE alphabetical prefix rather than an arbitrary subset, and
// reports the overflow so the dashboard can say "and N more" instead of
// quietly showing part of the truth.
func TestSelector_CapsManagedRowsPerSection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	const overflow = 5

	names := make([]string, 0, maxManagedResourcesPerSection+overflow)

	for i := range maxManagedResourcesPerSection + overflow {
		name := fmt.Sprintf("service-%04d", i)
		names = append(names, name)

		check := models.NewCheck(org.UID, name, "http")
		check.Name = &name
		r.NoError(svc.db.CreateCheck(ctx, check))
	}

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	resources, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(resources, maxManagedResourcesPerSection)

	// The kept rows are the alphabetical prefix — deterministic, so two
	// reconciles cannot swap which checks are shown.
	byUID := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		byUID[*resource.CheckUID] = struct{}{}
	}

	sort.Strings(names)
	kept := names[:maxManagedResourcesPerSection]
	dropped := names[maxManagedResourcesPerSection:]

	for _, name := range kept {
		check, getErr := svc.db.GetCheckByUidOrSlug(ctx, org.UID, name)
		r.NoError(getErr)
		r.Contains(byUID, check.UID, name)
	}

	for _, name := range dropped {
		check, getErr := svc.db.GetCheckByUidOrSlug(ctx, org.UID, name)
		r.NoError(getErr)
		r.NotContains(byUID, check.UID, name)
	}

	// The overflow is reported, not hidden.
	sections, err := svc.ListSections(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Len(sections, 1)
	r.True(sections[0].SelectorTruncated)
	r.Equal(maxManagedResourcesPerSection+overflow, sections[0].SelectorMatchTotal)
}

// --- The selector owns its rows: refusals, at the HTTP boundary ---
//
// These assert the STATUS CODE, not just the error value. A service-level
// assertion cannot see which mapper the handler routed the error through, and
// that gap is exactly how the delete path came to answer 500 INTERNAL_ERROR —
// a deliberate refusal reported as a server fault — while a green service test
// said the refusal worked.

// newResourceRequest builds an httptest request with the chi route params the
// real router sets for the section-resource endpoints.
func newResourceRequest(
	method, orgSlug, pageUID, sectionUID, resourceUID, body string,
) (*http.Request, *httptest.ResponseRecorder) {
	target := "/api/v1/orgs/" + orgSlug + "/status-pages/" + pageUID +
		"/sections/" + sectionUID + "/resources/" + resourceUID

	req := httptest.NewRequestWithContext(
		context.Background(), method, target, strings.NewReader(body),
	)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org", orgSlug)
	rctx.URLParams.Add("statusPageUid", pageUID)
	rctx.URLParams.Add("sectionUid", sectionUID)
	rctx.URLParams.Add("resourceUid", resourceUID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	return req, httptest.NewRecorder()
}

// seedManagedRow builds a page whose single section selects everything, plus
// one manual row and one managed row, and returns both.
func seedManagedRow(
	ctx context.Context, t *testing.T, svc *Service, org *models.Organization,
) (StatusPageResponse, string, *models.StatusPageResource, *models.StatusPageResource) {
	t.Helper()

	r := require.New(t)

	var manual, managed *models.StatusPageResource

	page, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	pinned := seedLabeledCheck(ctx, t, svc, org.UID, "Aaa pinned", nil)
	seedLabeledCheck(ctx, t, svc, org.UID, "Zzz auto", nil)

	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckUID: pinned.UID})
	r.NoError(err)

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	resources, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(resources, 2)

	for _, resource := range resources {
		if resource.ManagedBySelector {
			managed = resource
		} else {
			manual = resource
		}
	}

	r.NotNil(manual)
	r.NotNil(managed)

	return page, section.UID, manual, managed
}

// TestDeleteManagedResource_AnswersConflict pins the HTTP status of the
// refusal. The 409 branch lives in handleResourceError; the delete handler
// used to route through handleSectionError, which has no case for it, so the
// documented 409 was unreachable and clients saw a 500.
func TestDeleteManagedResource_AnswersConflict(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, sectionUID, manual, managed := seedManagedRow(ctx, t, svc, org)

	handler := NewHandler(svc, &config.Config{})

	req, rec := newResourceRequest(http.MethodDelete, org.Slug, page.UID, sectionUID, managed.UID, "")
	r.NoError(handler.DeleteResource(rec, req))
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "CONFLICT")

	// And the row really is still there — a refusal that quietly deleted
	// anyway would be worse than the 500 it replaced.
	resources, err := svc.db.ListStatusPageResources(ctx, sectionUID)
	r.NoError(err)
	r.Len(resources, 2)

	// Positive control: the MANUAL row on the same section still deletes, so
	// the guard is about ownership rather than about dynamic sections.
	//
	// The returned error is deliberately not asserted here: the 204 path calls
	// WriteJSON(StatusNoContent, nil), and net/http refuses a body on a 204.
	// That is pre-existing behavior shared by every DELETE handler and is
	// invisible over the real router; the status code is what this test is
	// about.
	req, rec = newResourceRequest(http.MethodDelete, org.Slug, page.UID, sectionUID, manual.UID, "")
	_ = handler.DeleteResource(rec, req)
	r.Equal(http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestUpdateManagedResource_PositionAnswersConflict pins that a bare position
// write on a managed row is refused. It self-corrected on the next reconcile
// before, which means the API said 200 and then silently undid the change.
func TestUpdateManagedResource_PositionAnswersConflict(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, sectionUID, manual, managed := seedManagedRow(ctx, t, svc, org)

	handler := NewHandler(svc, &config.Config{})

	req, rec := newResourceRequest(
		http.MethodPatch, org.Slug, page.UID, sectionUID, managed.UID, `{"position":1}`)
	r.NoError(handler.UpdateResource(rec, req))
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())

	stored, err := svc.db.GetStatusPageResource(ctx, sectionUID, managed.UID)
	r.NoError(err)
	r.Equal(managed.Position, stored.Position)

	// The cosmetic fields stay writable — the reconciler never touches them,
	// so there is nothing for the operator's edit to fight with.
	req, rec = newResourceRequest(
		http.MethodPatch, org.Slug, page.UID, sectionUID, managed.UID, `{"publicName":"Checkout"}`)
	r.NoError(handler.UpdateResource(rec, req))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// Positive control: repositioning the MANUAL row is still allowed.
	req, rec = newResourceRequest(
		http.MethodPatch, org.Slug, page.UID, sectionUID, manual.UID, `{"position":3}`)
	r.NoError(handler.UpdateResource(rec, req))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
}

// TestReorderResources_RefusesMovingAManagedRow pins the narrow rule: manual
// rows stay freely reorderable inside a dynamic section, but an order that
// moves a managed row out of the reconciler's arrangement is refused rather
// than accepted-then-reverted.
func TestReorderResources_RefusesMovingAManagedRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	// Two manual rows and two managed ones, so both halves are exercised.
	first := seedLabeledCheck(ctx, t, svc, org.UID, "Aaa manual one", nil)
	second := seedLabeledCheck(ctx, t, svc, org.UID, "Bbb manual two", nil)
	seedLabeledCheck(ctx, t, svc, org.UID, "Yyy auto one", nil)
	seedLabeledCheck(ctx, t, svc, org.UID, "Zzz auto two", nil)

	for _, check := range []*models.Check{first, second} {
		_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
			CreateResourceRequest{CheckUID: check.UID})
		r.NoError(err)
	}

	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	resources, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(resources, 4)

	uids := make([]string, len(resources))
	for i, resource := range resources {
		uids[i] = resource.UID
	}

	r.False(resources[0].ManagedBySelector)
	r.False(resources[1].ManagedBySelector)
	r.True(resources[2].ManagedBySelector)
	r.True(resources[3].ManagedBySelector)

	handler := NewHandler(svc, &config.Config{})

	reorder := func(order []string) *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(ReorderResourcesRequest{UIDs: order})
		r.NoError(marshalErr)

		target := "/api/v1/orgs/" + org.Slug + "/status-pages/" + page.UID +
			"/sections/" + section.UID + "/resources/reorder"
		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodPost, target, strings.NewReader(string(body)))

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("org", org.Slug)
		rctx.URLParams.Add("statusPageUid", page.UID)
		rctx.URLParams.Add("sectionUid", section.UID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rec := httptest.NewRecorder()
		// Same 204-with-no-body caveat as DeleteResource above; the status
		// code is the assertion.
		_ = handler.ReorderResources(rec, req)

		return rec
	}

	// Hoisting a managed row above the manual ones: refused.
	rec := reorder([]string{uids[2], uids[0], uids[1], uids[3]})
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())

	// Swapping the two managed rows among themselves: also refused — the
	// selector decides their order, not the client.
	rec = reorder([]string{uids[0], uids[1], uids[3], uids[2]})
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())

	// Positive control: swapping the two MANUAL rows, which is what the
	// dashboard's drag-and-drop actually sends, still works.
	rec = reorder([]string{uids[1], uids[0], uids[2], uids[3]})
	r.Equal(http.StatusNoContent, rec.Code, rec.Body.String())

	reordered, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Equal(uids[1], reordered[0].UID)
	r.Equal(uids[0], reordered[1].UID)
}

// TestSelector_ManualAddOverAManagedRow pins the other direction of
// "manual placement wins": pinning a check the selector has ALREADY
// materialized in that section must succeed and hand ownership over.
//
// It used to fail on the (section_uid, check_uid) unique index — the operator
// asked to pin a component and got a database constraint error back.
func TestSelector_ManualAddOverAManagedRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, section := seedSelectorPage(ctx, t, svc, org, models.SectionSelector{All: true})

	check := seedLabeledCheck(ctx, t, svc, org.UID, "Checkout", nil)
	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	resources, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(resources, 1)
	r.True(resources[0].ManagedBySelector, "precondition: the selector got there first")

	name := "Checkout (pinned)"
	created, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckUID: check.UID, PublicName: &name})
	r.NoError(err)
	r.False(created.ManagedBySelector)

	// Exactly one row, and it is the operator's.
	resources, err = svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(resources, 1)
	r.False(resources[0].ManagedBySelector)
	r.Equal(name, *resources[0].PublicName)

	// And a further reconcile leaves it alone rather than re-adopting.
	r.NoError(svc.ReconcilePage(ctx, org.UID, page.UID))

	resources, err = svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(resources, 1)
	r.False(resources[0].ManagedBySelector)
}
