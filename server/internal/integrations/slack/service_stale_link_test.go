package slack

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// staleTeamID stands in for the workspace whose install was bricked in the
// field: its organization was soft-deleted before DeleteOrg started releasing
// provider links, so the (slack, team_id) row outlived the org it names.
const staleTeamID = "T0ACME0003"

// installedOrg resolves the organization an install landed in.
func installedOrg(ctx context.Context, t *testing.T, svc *Service, slug string) *models.Organization {
	t.Helper()

	org, err := svc.db.GetOrganizationBySlug(ctx, slug)
	require.NoError(t, err)

	return org
}

// liveOrgCount counts organizations that still resolve, so a test can prove an
// install created a SECOND org rather than merely returning a different UID.
func liveOrgCount(ctx context.Context, t *testing.T, svc *Service) int {
	t.Helper()

	orgs, err := svc.db.ListOrganizations(ctx)
	require.NoError(t, err)

	return len(orgs)
}

// TestInstallHealsStaleOrgLinkAndCreatesFreshOrg is the failing case this spec
// exists for. Before the fix the install dereferenced the link with a bare
// GetOrganization, so a link pointing at a soft-deleted org answered
// sql.ErrNoRows on EVERY retry and the workspace could never be reinstalled.
//
// The heal is "clear and create", never "resurrect": the deletion was an
// explicit act, so the workspace gets a NEW org, and the dangling row is left
// cleared behind it.
func TestInstallHealsStaleOrgLinkAndCreatesFreshOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, staleTeamID, nil)

	first := runInstallCallback(ctx, t, svc)
	original := installedOrg(ctx, t, svc, first.OrgSlug)

	staleLink, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, staleTeamID)
	r.NoError(err)
	r.Equal(original.UID, staleLink.OrganizationUID)

	// Exactly the field state: the org is soft-deleted and its link is NOT
	// released (the deletion predates the release step in DeleteOrg).
	r.NoError(svc.db.DeleteOrganization(ctx, original.UID))

	stillLive, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, staleTeamID)
	r.NoError(err, "the link must still win the lookup — that is what made this fatal")
	r.Equal(staleLink.UID, stillLive.UID)

	// The install this used to fail on.
	second := runInstallCallback(ctx, t, svc)
	healed := installedOrg(ctx, t, svc, second.OrgSlug)

	r.NotEqual(original.UID, healed.UID, "the reinstall must land in a FRESH org, not the deleted one")
	r.Equal(1, liveOrgCount(ctx, t, svc), "the soft-deleted org must not come back")

	// The link now names the new org, and the dangling row is gone.
	relinked, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, staleTeamID)
	r.NoError(err)
	r.Equal(healed.UID, relinked.OrganizationUID)
	r.NotEqual(staleLink.UID, relinked.UID)

	_, err = svc.db.GetOrganizationProvider(ctx, staleLink.UID)
	r.ErrorIs(err, sql.ErrNoRows, "the stale link must be left cleared, not live alongside the new one")
}

// TestInstallReusesLiveOrgLink is the positive control: with the linked org
// still alive, the SAME install path must reuse it and create no second org.
// Without this, a heal that fired unconditionally would pass the test above.
func TestInstallReusesLiveOrgLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, staleTeamID, nil)

	first := runInstallCallback(ctx, t, svc)
	original := installedOrg(ctx, t, svc, first.OrgSlug)

	link, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, staleTeamID)
	r.NoError(err)

	second := runInstallCallback(ctx, t, svc)
	r.Equal(first.OrgSlug, second.OrgSlug)
	r.Equal(original.UID, installedOrg(ctx, t, svc, second.OrgSlug).UID)
	r.Equal(1, liveOrgCount(ctx, t, svc), "reinstalling a live workspace must not mint a second org")

	unchanged, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, staleTeamID)
	r.NoError(err)
	r.Equal(link.UID, unchanged.UID, "a live link must be left untouched")
}

// TestInstallHealIsIdempotent pins that the heal happens once: the install
// AFTER the healing one reuses the org the heal created instead of clearing a
// perfectly good link and minting a third org.
func TestInstallHealIsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, staleTeamID, nil)

	first := runInstallCallback(ctx, t, svc)
	r.NoError(svc.db.DeleteOrganization(ctx, installedOrg(ctx, t, svc, first.OrgSlug).UID))

	second := runInstallCallback(ctx, t, svc)
	healed := installedOrg(ctx, t, svc, second.OrgSlug)

	healedLink, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, staleTeamID)
	r.NoError(err)

	third := runInstallCallback(ctx, t, svc)
	r.Equal(healed.UID, installedOrg(ctx, t, svc, third.OrgSlug).UID)
	r.Equal(1, liveOrgCount(ctx, t, svc), "a third install must not mint a third org")

	sameLink, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, staleTeamID)
	r.NoError(err)
	r.Equal(healedLink.UID, sameLink.UID)
}

// TestInstallRelinksUserWhoseLinkWentStale covers the mirror gap. The user
// lookup is by EMAIL, so a soft-deleted user yields a freshly created one — but
// the stale user_providers row pointing at the dead user used to take the
// "already linked" branch, leaving that new user with NO Slack identity at all.
// The install still succeeded, which is what made it so quiet.
func TestInstallRelinksUserWhoseLinkWentStale(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, staleTeamID, nil)

	first := runInstallCallback(ctx, t, svc)

	staleLink, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, "U-installer")
	r.NoError(err)
	r.Equal(first.UserUID, staleLink.UserUID)

	// The account goes, the link does not: user_providers has no deleted_at and
	// is never cascaded by a soft delete.
	r.NoError(svc.db.DeleteUser(ctx, first.UserUID))

	second := runInstallCallback(ctx, t, svc)
	r.NotEqual(first.UserUID, second.UserUID, "the deleted account must not be reused")

	relinked, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, "U-installer")
	r.NoError(err, "the fresh user must end up linked, not silently identity-less")
	r.Equal(second.UserUID, relinked.UserUID)
	r.NotEqual(staleLink.UID, relinked.UID)
}

// TestInstallReusesLiveUserLink is the positive control for the heal above: an
// existing, live link is reused as-is and never re-created.
func TestInstallReusesLiveUserLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, staleTeamID, nil)

	first := runInstallCallback(ctx, t, svc)

	link, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, "U-installer")
	r.NoError(err)

	second := runInstallCallback(ctx, t, svc)
	r.Equal(first.UserUID, second.UserUID)

	unchanged, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, "U-installer")
	r.NoError(err)
	r.Equal(link.UID, unchanged.UID, "a live user link must be left untouched")
}
