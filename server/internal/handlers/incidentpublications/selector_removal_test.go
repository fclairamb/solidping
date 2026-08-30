package incidentpublications_test

// Managed-vs-manual removal parity (spec 2026-08-29-11).
//
// A status page section may carry a SELECTOR, and the reconciler materializes
// real status_page_resources rows for it. When a check stops matching, the
// reconciler removes its row through the SAME call an operator's manual delete
// uses (db.DeleteStatusPageResource) — which is the whole reason the spec asks
// for "the same path a manual resource delete uses".
//
// What that buys, and what this test pins, is that a PAST publication's
// affectedResources display cannot behave differently depending on how the row
// went away. The test does not assume what the behaviour is; it MEASURES the
// manual path and requires the managed path to match it exactly. If a future
// change makes resource removal retain names on past publications, this test
// keeps passing — and keeps the two paths honest.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

// affectedAfter runs one full "incident opens, publishes, resolves" cycle with
// the check displayed on the page, records what the resolved publication shows
// as affected, then applies `remove` and records it again.
func affectedAfter(
	t *testing.T,
	attach func(ctx context.Context, setup *pubSetup, svc *statuspages.Service),
	remove func(ctx context.Context, setup *pubSetup, svc *statuspages.Service),
) (before, after []string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	// noResource: this test builds the page resource itself, manually or via
	// the selector, so the two variants differ in exactly one way.
	setup := newPubSetup(t, setupOptions{autoPublish: true, noResource: true})

	// resourceNamesOnPage falls back to the CHECK's display name when the
	// resource carries no publicName — and a materialized row never carries
	// one, so the name has to come from the check for the comparison to be
	// about removal rather than about naming.
	name := "Payments API"
	r.NoError(setup.dbSvc.UpdateCheck(ctx, setup.check.UID, &models.CheckUpdate{Name: &name}))

	pagesSvc := statuspages.NewService(setup.dbSvc, nil, nil)
	attach(ctx, setup, pagesSvc)

	// Open, publish, resolve.
	setup.submit(models.ResultStatusDown)
	setup.submit(models.ResultStatusUp)

	incidents, err := setup.pubs.ListPublicIncidents(ctx, setup.page, false)
	r.NoError(err)
	r.Len(incidents, 1, "precondition: the incident must have published")

	before = incidents[0].AffectedResources

	remove(ctx, setup, pagesSvc)

	incidents, err = setup.pubs.ListPublicIncidents(ctx, setup.page, false)
	r.NoError(err)
	r.Len(incidents, 1, "removing a resource must never remove the publication itself")

	return before, incidents[0].AffectedResources
}

// TestAffectedResources_ManagedRemovalMirrorsManualRemoval is the parity test
// spec 2026-08-29-11 asks for: whatever a manual delete guarantees for a past
// publication's affected-resource display, a selector-driven removal
// guarantees identically.
func TestAffectedResources_ManagedRemovalMirrorsManualRemoval(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// --- Manual: the operator adds the component, then deletes it. ---
	manualBefore, manualAfter := affectedAfter(t,
		func(ctx context.Context, setup *pubSetup, _ *statuspages.Service) {
			resource := models.NewStatusPageResource(setup.section.UID, setup.check.UID, 1)
			require.NoError(t, setup.dbSvc.CreateStatusPageResource(ctx, resource))
			setup.resource = resource
		},
		func(ctx context.Context, setup *pubSetup, _ *statuspages.Service) {
			require.NoError(t, setup.dbSvc.DeleteStatusPageResource(ctx, setup.resource.UID))
		},
	)

	// --- Managed: a label selector materializes the component, then the label
	// is removed and the reconciler takes it away. ---
	managedBefore, managedAfter := affectedAfter(t,
		func(ctx context.Context, setup *pubSetup, svc *statuspages.Service) {
			labelCheck(ctx, t, setup, "public", "true")

			selector := &models.SectionSelector{Labels: map[string]string{"public": "true"}}
			require.NoError(t, setup.dbSvc.UpdateStatusPageSection(ctx, setup.section.UID,
				&models.StatusPageSectionUpdate{SetSelector: true, Selector: selector}))

			require.NoError(t, svc.ReconcilePage(ctx, setup.org.UID, setup.page.UID))

			resources, err := setup.dbSvc.ListStatusPageResources(ctx, setup.section.UID)
			require.NoError(t, err)
			require.Len(t, resources, 1)
			require.True(t, resources[0].ManagedBySelector,
				"precondition: the row must be selector-owned, not manual")
		},
		func(ctx context.Context, setup *pubSetup, svc *statuspages.Service) {
			// The check stops matching.
			require.NoError(t, setup.dbSvc.SetCheckLabels(ctx, setup.check.UID, nil))
			require.NoError(t, svc.ReconcilePage(ctx, setup.org.UID, setup.page.UID))

			resources, err := setup.dbSvc.ListStatusPageResources(ctx, setup.section.UID)
			require.NoError(t, err)
			require.Empty(t, resources, "precondition: the reconciler must have removed the row")
		},
	)

	// Positive control: both variants really did display the component while
	// it existed. Without this, two empty "after" lists would agree for the
	// uninteresting reason that nothing was ever shown.
	r.Equal([]string{"Payments API"}, manualBefore)
	r.Equal([]string{"Payments API"}, managedBefore)

	// The actual claim. As measured today both are empty: resources are HARD
	// deleted (status_page_resources has no deleted_at) and affectedResources is
	// resolved at READ time from live rows, so removing a component drops it
	// from past publications too — for manual and managed removals alike. That
	// is stated as an observation, not baked into the assertion: the test
	// compares the two paths, so it stays correct if the behaviour changes.
	r.Equal(manualAfter, managedAfter,
		"a selector-driven removal must leave a past publication in exactly the state a manual delete does")
}

// labelCheck attaches one key=value label to the setup's check, the same way
// the checks service does (GetOrCreateLabel + SetCheckLabels).
func labelCheck(ctx context.Context, t *testing.T, setup *pubSetup, key, value string) {
	t.Helper()

	label, err := setup.dbSvc.GetOrCreateLabel(ctx, setup.org.UID, key, value)
	require.NoError(t, err)
	require.NoError(t, setup.dbSvc.SetCheckLabels(ctx, setup.check.UID, []string{label.UID}))
}
