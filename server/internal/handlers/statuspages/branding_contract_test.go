package statuspages

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// TestBrandingPayloadIsByteIdenticalAfterTheStorageMove is what proves spec
// 2026-08-22-03 is a REFACTOR and not a feature change.
//
// Branding moved from three columns into `settings -> branding`, and the public
// asset route moved from a per-feature state query to a topic allowlist. None
// of that may reach a client: `logoUrl`, `faviconUrl` and `hideBranding` keep
// their exact names, their exact nullability, and — crucially — the exact
// `/pub/status-page-assets/<file uid>` URL shape, on both the org-facing and the
// public payload.
func TestBrandingPayloadIsByteIdenticalAfterTheStorageMove(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSelfHosted)
	page := createPublicPage(ctx, t, dbService, org.UID)

	const (
		logoUID    = "aaaaaaaa-1111-2222-3333-444444444444"
		faviconUID = "bbbbbbbb-1111-2222-3333-444444444444"
	)

	logo := logoUID
	favicon := faviconUID
	r.NoError(dbService.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID: &logo, FaviconFileUID: &favicon, HideBranding: true,
	}))

	// This test is about the payload SHAPE, but hideBranding only survives to
	// the public view while the org holds the entitlement — and no deployment
	// mode grants it by default any more. Grant it explicitly.
	ent := entitlements.NewService(dbService, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(ent.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{WhiteLabel: entitlements.Bool(true)},
		Source: models.EntitlementSourceAdmin,
	}, "test", "operator grant"))

	admin, err := svc.GetStatusPage(ctx, "acme", testPublicSlug, GetStatusPageOptions{})
	r.NoError(err)

	adminJSON := marshalMap(t, admin)
	r.Equal("/pub/status-page-assets/"+logoUID, adminJSON["logoUrl"])
	r.Equal("/pub/status-page-assets/"+faviconUID, adminJSON["faviconUrl"])
	r.Equal(true, adminJSON["hideBranding"])

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)

	publicJSON := marshalMap(t, public)
	r.Equal("/pub/status-page-assets/"+logoUID, publicJSON["logoUrl"])
	r.Equal("/pub/status-page-assets/"+faviconUID, publicJSON["faviconUrl"])
	r.Equal(true, publicJSON["hideBranding"])

	// The storage move must not have leaked its shape onto the wire: no
	// `settings.branding` key, and no raw file UIDs under new names.
	settings, hasSettings := adminJSON["settings"].(map[string]any)
	if hasSettings {
		_, leaked := settings["branding"]
		r.False(leaked, "settings.branding is storage, not a public contract")
	}

	r.NotContains(adminJSON, "logoFileUid")
	r.NotContains(publicJSON, "logoFileUid")
}

// TestClearedBrandingIsOmittedNotEmpty pins the other half of the contract: an
// unset slot produces NO url key rather than an empty string that would 404 on
// every page load, and hideBranding is present as an explicit false.
func TestClearedBrandingIsOmittedNotEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSelfHosted)
	createPublicPage(ctx, t, dbService, org.UID)

	admin, err := svc.GetStatusPage(ctx, "acme", testPublicSlug, GetStatusPageOptions{})
	r.NoError(err)

	adminJSON := marshalMap(t, admin)
	r.NotContains(adminJSON, "logoUrl")
	r.NotContains(adminJSON, "faviconUrl")
	r.Equal(false, adminJSON["hideBranding"])

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)

	publicJSON := marshalMap(t, public)
	r.NotContains(publicJSON, "logoUrl")
	r.NotContains(publicJSON, "faviconUrl")
	r.Equal(false, publicJSON["hideBranding"])
}

// marshalMap round-trips a response through encoding/json, so the assertions
// above are about the BYTES a client receives rather than about Go fields.
func marshalMap(t *testing.T, value any) map[string]any {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	var out map[string]any

	require.NoError(t, json.Unmarshal(data, &out))

	return out
}
