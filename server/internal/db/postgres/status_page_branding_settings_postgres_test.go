package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portStatusPageBrandingSettings is this test's own embedded-postgres port.
const portStatusPageBrandingSettings = 15488

// brandingSettingsPG boots an embedded cluster with one org and returns it.
func brandingSettingsPG(t *testing.T) (context.Context, *Service, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portStatusPageBrandingSettings, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := "88888888-8888-8888-8888-888888888888"

	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acmesettings', 'Acme Settings')
		 on conflict (uid) do nothing`, org)
	r.NoError(err)

	return ctx, svc, org
}

func brandingPagePG(ctx context.Context, t *testing.T, svc *Service, orgUID, slug string) *models.StatusPage {
	t.Helper()

	page := models.NewStatusPage(orgUID, "Acme Status", slug)
	up := 99.95
	degraded := 98.5
	page.Settings.Availability = &models.AvailabilitySettings{ThresholdUp: &up, ThresholdDegraded: &degraded}

	require.NoError(t, svc.CreateStatusPage(ctx, page))

	return page
}

func reloadPG(ctx context.Context, t *testing.T, svc *Service, orgUID, pageUID string) *models.StatusPage {
	t.Helper()

	page, err := svc.GetStatusPage(ctx, orgUID, pageUID)
	require.NoError(t, err)
	require.NotNil(t, page)

	return page
}

// TestBrandingSettingsMerge_Postgres is the Postgres half of the clobber test.
//
// The merge is written DIFFERENTLY in each dialect — SQLite gets json_patch
// (recursive), Postgres gets a two-level jsonb_build_object/`||` because `||`
// is shallow — so "the SQLite one passes" says nothing about this one. A
// one-level `settings || '{"branding":…}'` here would replace the whole branding
// object and this test would catch it.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestBrandingSettingsMerge_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	ctx, svc, org := brandingSettingsPG(t)

	// --- Branding must not clobber availability. ---
	page := brandingPagePG(ctx, t, svc, org, "pg-clobber-one")

	logo := "logo-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID: &logo, HideBranding: true,
	}))

	got := reloadPG(ctx, t, svc, org, page.UID)
	r.Equal(logo, *got.Settings.LogoFileUID())
	r.True(got.Settings.HideBranding())
	r.NotNil(got.Settings.Availability, "the branding merge clobbered the availability section")
	r.InDelta(99.95, *got.Settings.Availability.ThresholdUp, 0.0001)
	r.InDelta(98.5, *got.Settings.Availability.ThresholdDegraded, 0.0001)

	// --- ...and availability must not clobber branding. ---
	next := got.Settings
	newUp := 99.5
	next.Availability = &models.AvailabilitySettings{ThresholdUp: &newUp}
	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{Settings: &next}))

	got = reloadPG(ctx, t, svc, org, page.UID)
	r.InDelta(99.5, *got.Settings.Availability.ThresholdUp, 0.0001)
	r.Equal(logo, *got.Settings.LogoFileUID(), "an availability write clobbered the branding section")
	r.True(got.Settings.HideBranding())

	// --- A second asset slot, written without mentioning the first. ---
	favicon := "favicon-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID: &logo, FaviconFileUID: &favicon, HideBranding: true,
	}))

	// --- Clearing: Postgres STORES json null where SQLite REMOVES the key.
	// Both must decode to a nil *string — that parity is the point. ---
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID: nil, FaviconFileUID: &favicon, HideBranding: true,
	}))

	got = reloadPG(ctx, t, svc, org, page.UID)
	r.Nil(got.Settings.LogoFileUID(), "a cleared slot must decode to nil whatever the engine stored")
	r.Equal(favicon, *got.Settings.FaviconFileUID())
	r.NotNil(got.Settings.Availability, "two merges later, the thresholds are still there")

	// Pin the physical difference so the parity above is a real claim about two
	// storage shapes and not two identical ones: Postgres keeps the key, null.
	var hasKey bool

	// jsonb_exists(), not the `?` operator: `?` is bun's placeholder marker.
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select jsonb_exists(settings->'branding', 'logoFileUid') from status_pages where uid = ?`,
		page.UID).Scan(&hasKey))
	r.True(hasKey, "Postgres's || stores an explicit null rather than removing the key")
}

// TestHideBrandingPatch_Postgres covers the generic PATCH path and the fold:
// a whole-column Settings write and a HideBranding flip in the SAME update
// would be two assignments to one column, which Postgres refuses outright.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestHideBrandingPatch_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	ctx, svc, org := brandingSettingsPG(t)
	page := brandingPagePG(ctx, t, svc, org, "pg-hide-one")

	logo := "logo-file-uid"
	r.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{LogoFileUID: &logo}))

	// Flip alone: a one-key merge that must leave the asset slot and the
	// thresholds intact.
	hide := true
	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &hide}))

	got := reloadPG(ctx, t, svc, org, page.UID)
	r.True(got.Settings.HideBranding())
	r.Equal(logo, *got.Settings.LogoFileUID(), "flipping hideBranding must not drop the logo")
	r.NotNil(got.Settings.Availability)

	// Flip together with a whole-column settings write: this is the statement
	// that would fail with "multiple assignments to same column" if the fold
	// were removed.
	next := got.Settings
	newUp := 99.0
	next.Availability = &models.AvailabilitySettings{ThresholdUp: &newUp}
	hide = false

	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{Settings: &next, HideBranding: &hide}))

	got = reloadPG(ctx, t, svc, org, page.UID)
	r.False(got.Settings.HideBranding(), "the folded flag must win")
	r.InDelta(99.0, *got.Settings.Availability.ThresholdUp, 0.0001)
	r.Equal(logo, *got.Settings.LogoFileUID())
}
