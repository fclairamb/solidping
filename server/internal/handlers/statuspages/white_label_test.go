package statuspages

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// whiteLabelSetup builds a status-pages service backed by a REAL entitlements
// service seeded with the given deployment mode's defaults, so the tests below
// exercise the same resolver production uses rather than a stub that always
// says yes.
func whiteLabelSetup(
	t *testing.T, mode string,
) (context.Context, db.Service, *Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	ent := entitlements.NewService(dbService, entitlements.DefaultsFor(mode), 0)

	return ctx, dbService, NewService(dbService, &config.Config{}, ent), org
}

func createPublicPage(ctx context.Context, t *testing.T, dbService db.Service, orgUID string) *models.StatusPage {
	t.Helper()

	page := models.NewStatusPage(orgUID, "Acme Status", testPublicSlug)
	require.NoError(t, dbService.CreateStatusPage(ctx, page))

	return page
}

// TestSelfHostedWithoutTheEntitlementKeepsTheBadge pins the self-hosted
// default. White labeling is what a paid plan buys, so a self-hosted instance
// does not get it for free either — opting the page in is not enough.
func TestSelfHostedWithoutTheEntitlementKeepsTheBadge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSelfHosted)
	page := createPublicPage(ctx, t, dbService, org.UID)

	optIn := true
	r.NoError(dbService.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &optIn}))

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)
	r.False(public.HideBranding, "self-hosted + opted in must still show the badge")
	r.Nil(public.WhiteLabelAllowed, "the public payload must never disclose plan state")
}

// TestSelfHostedGrantedTheEntitlementDropsTheBadge is the positive control for
// self-hosted: an operator who grants the entitlement on their own instance
// gets the unbranded page. Being AGPL, that door stays open by design.
func TestSelfHostedGrantedTheEntitlementDropsTheBadge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSelfHosted)
	page := createPublicPage(ctx, t, dbService, org.UID)

	optIn := true
	r.NoError(dbService.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &optIn}))

	ent := entitlements.NewService(dbService, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(ent.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{WhiteLabel: entitlements.Bool(true)},
		Source: models.EntitlementSourceAdmin,
	}, "test", "operator grant"))

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)
	r.True(public.HideBranding, "an explicit grant must drop the badge")
}

// TestSaaSWithoutTheEntitlementKeepsTheBadge is the negative control that
// makes the feature worth money: the page opted in, and the badge stays,
// because the SaaS Free defaults do not include white labeling.
func TestSaaSWithoutTheEntitlementKeepsTheBadge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSaaS)
	page := createPublicPage(ctx, t, dbService, org.UID)

	optIn := true
	r.NoError(dbService.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &optIn}))

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)
	r.False(public.HideBranding, "opting in must not be enough on a plan without the entitlement")

	// ...and the admin payload still reports the raw opt-in, so the toggle
	// round-trips honestly and an upgrade needs no re-tick.
	admin, err := svc.GetStatusPage(ctx, "acme", testPublicSlug, GetStatusPageOptions{})
	r.NoError(err)
	r.True(admin.HideBranding, "the stored opt-in must survive on the admin payload")
	r.NotNil(admin.WhiteLabelAllowed)
	r.False(*admin.WhiteLabelAllowed)
}

// TestSaaSGrantedTheEntitlementDropsTheBadge closes the loop: writing the
// entitlement (what billing does on an upgrade) is enough — nothing on the
// page has to change.
func TestSaaSGrantedTheEntitlementDropsTheBadge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSaaS)
	page := createPublicPage(ctx, t, dbService, org.UID)

	optIn := true
	r.NoError(dbService.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &optIn}))

	ent := entitlements.NewService(dbService, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0)
	r.NoError(ent.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{WhiteLabel: entitlements.Bool(true)},
		Source: models.EntitlementSourceBilling,
	}, "test", "upgrade"))

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)
	r.True(public.HideBranding)
}

// TestEntitledButNotOptedInKeepsTheBadge is the other half of the AND: paying
// for white labeling does not silently unbrand every page an org owns.
func TestEntitledButNotOptedInKeepsTheBadge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSelfHosted)
	createPublicPage(ctx, t, dbService, org.UID)

	// Grant the entitlement explicitly — no deployment mode hands it out by
	// default any more, so without this the assertion would pass for the wrong
	// reason (missing entitlement rather than missing opt-in).
	ent := entitlements.NewService(dbService, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(ent.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{WhiteLabel: entitlements.Bool(true)},
		Source: models.EntitlementSourceAdmin,
	}, "test", "operator grant"))

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)
	r.False(public.HideBranding, "the entitlement alone must not hide the badge")
}

// TestWhiteLabelFailsClosedWithoutAnEntitlementsService pins the nil-service
// fallback. Every caller here is on a render path, so the honest answer when
// entitlements cannot be consulted is "show the badge".
func TestWhiteLabelFailsClosedWithoutAnEntitlementsService(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	page := createPublicPage(ctx, t, dbService, org.UID)

	optIn := true
	r.NoError(dbService.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{HideBranding: &optIn}))

	svc := NewService(dbService, &config.Config{}, nil)

	public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)
	r.False(public.HideBranding)
}

// TestDefaultsForWhiteLabelPerMode pins the two defaults directly, so a change
// to either one is a deliberate edit to this test rather than a silent
// pricing change.
func TestDefaultsForWhiteLabelPerMode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	selfHosted := entitlements.DefaultsFor(config.DeploymentModeSelfHosted)
	r.NotNil(selfHosted.Limits.WhiteLabel)
	r.False(*selfHosted.Limits.WhiteLabel, "self-hosted is not entitled by default")

	saas := entitlements.DefaultsFor(config.DeploymentModeSaaS)
	r.NotNil(saas.Limits.WhiteLabel)
	r.False(*saas.Limits.WhiteLabel, "the SaaS Free tier is not entitled by default")
}
