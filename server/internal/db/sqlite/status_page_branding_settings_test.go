package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// brandingPage creates a page with availability thresholds already set, which
// is the sibling section every branding write must leave alone.
func brandingPage(ctx context.Context, t *testing.T, svc *Service, orgUID, slug string) *models.StatusPage {
	t.Helper()

	page := models.NewStatusPage(orgUID, "Acme Status", slug)
	up := 99.95
	degraded := 98.5
	page.Settings.Availability = &models.AvailabilitySettings{ThresholdUp: &up, ThresholdDegraded: &degraded}

	require.NoError(t, svc.CreateStatusPage(ctx, page))

	return page
}

func reload(ctx context.Context, t *testing.T, svc *Service, orgUID, pageUID string) *models.StatusPage {
	t.Helper()

	page, err := svc.GetStatusPage(ctx, orgUID, pageUID)
	require.NoError(t, err)
	require.NotNil(t, page)

	return page
}

// TestBrandingWriteDoesNotClobberAvailability is THE negative control for this
// storage move (spec 2026-08-22-03).
//
// Branding and availability now share one column. The obvious implementation —
// read StatusPageSettings, mutate .Branding, write the struct back — passes
// every branding test and silently discards the operator's availability
// thresholds. This is the test that refuses it: the write is a JSON merge in
// SQL, so the sibling section survives.
func TestBrandingWriteDoesNotClobberAvailability(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)
	page := brandingPage(ctx, t, svc, org, "clobber-one")

	logo := "logo-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID:  &logo,
		HideBranding: true,
	}))

	got := reload(ctx, t, svc, org, page.UID)

	// The branding write landed...
	r.Equal(logo, *got.Settings.LogoFileUID())
	r.True(got.Settings.HideBranding())

	// ...and the thresholds it never mentioned are untouched.
	r.NotNil(got.Settings.Availability, "the branding merge clobbered the availability section")
	r.InDelta(99.95, *got.Settings.Availability.ThresholdUp, 0.0001)
	r.InDelta(98.5, *got.Settings.Availability.ThresholdDegraded, 0.0001)
}

// TestAvailabilityWriteDoesNotClobberBranding is the same property in the other
// direction: the settings PATCH path writes the whole column, so it has to
// carry the branding section it never mentions.
func TestAvailabilityWriteDoesNotClobberBranding(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)
	page := brandingPage(ctx, t, svc, org, "clobber-two")

	logo := "logo-file-uid"
	favicon := "favicon-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID:    &logo,
		FaviconFileUID: &favicon,
		HideBranding:   true,
	}))

	// What statuspages.Service does for a settings PATCH: start from the
	// CURRENT value, replace the availability section, write the whole column.
	current := reload(ctx, t, svc, org, page.UID)
	next := current.Settings
	newUp := 99.5
	next.Availability = &models.AvailabilitySettings{ThresholdUp: &newUp}

	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{Settings: &next}))

	got := reload(ctx, t, svc, org, page.UID)
	r.InDelta(99.5, *got.Settings.Availability.ThresholdUp, 0.0001)
	r.Equal(logo, *got.Settings.LogoFileUID(), "an availability write clobbered the branding section")
	r.Equal(favicon, *got.Settings.FaviconFileUID())
	r.True(got.Settings.HideBranding())
}

// TestClearingAnAssetDecodesToNil pins the dialect parity the spec calls out:
// SQLite's json_patch REMOVES a key patched to null while Postgres's `||`
// STORES null. The two disagree physically and must agree on what they decode
// to, or a cleared logo reads back as set on one engine.
func TestClearingAnAssetDecodesToNil(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)
	page := brandingPage(ctx, t, svc, org, "clear-one")

	logo := "logo-file-uid"
	favicon := "favicon-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID: &logo, FaviconFileUID: &favicon, HideBranding: true,
	}))

	// Clear the logo, keep the favicon — the whole-section write restates it.
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID: nil, FaviconFileUID: &favicon, HideBranding: true,
	}))

	got := reload(ctx, t, svc, org, page.UID)
	r.Nil(got.Settings.LogoFileUID(), "a cleared slot must decode to nil whatever the engine stored")
	r.Equal(favicon, *got.Settings.FaviconFileUID(), "the restated slot must survive")
	r.True(got.Settings.HideBranding())

	// And the availability section is STILL there after two merges.
	r.NotNil(got.Settings.Availability)
}

// TestHideBrandingPatchLeavesTheAssetSlots covers the generic PATCH path
// (models.StatusPageUpdate.HideBranding), which merges one key rather than
// writing the section: flipping the white-label opt-in must not drop the logo.
func TestHideBrandingPatchLeavesTheAssetSlots(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)
	page := brandingPage(ctx, t, svc, org, "hide-one")

	logo := "logo-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{LogoFileUID: &logo}))

	hide := true
	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &hide}))

	got := reload(ctx, t, svc, org, page.UID)
	r.True(got.Settings.HideBranding())
	r.Equal(logo, *got.Settings.LogoFileUID(), "flipping hideBranding must not drop the logo")
	r.NotNil(got.Settings.Availability, "flipping hideBranding must not drop the thresholds")

	// ...and back off again.
	hide = false
	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &hide}))

	got = reload(ctx, t, svc, org, page.UID)
	r.False(got.Settings.HideBranding())
	r.Equal(logo, *got.Settings.LogoFileUID())
}

// TestSettingsAndHideBrandingInOneUpdate is the guard against two `SET settings
// = ...` clauses in one statement: it is an outright error on Postgres, so the
// flag is folded into the whole-column value in Go. Both must land.
func TestSettingsAndHideBrandingInOneUpdate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)
	page := brandingPage(ctx, t, svc, org, "fold-one")

	logo := "logo-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{LogoFileUID: &logo}))

	current := reload(ctx, t, svc, org, page.UID)
	next := current.Settings
	newUp := 99.0
	next.Availability = &models.AvailabilitySettings{ThresholdUp: &newUp}
	hide := true

	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{
		Settings: &next, HideBranding: &hide,
	}))

	got := reload(ctx, t, svc, org, page.UID)
	r.True(got.Settings.HideBranding(), "the folded flag must land")
	r.InDelta(99.0, *got.Settings.Availability.ThresholdUp, 0.0001)
	r.Equal(logo, *got.Settings.LogoFileUID())
}
