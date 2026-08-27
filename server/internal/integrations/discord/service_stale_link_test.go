package discord

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// The Discord bot install carried the SAME unhealed dereference as its Slack
// twin — spec 2026-08-27-02's call-site audit is what found it. These tests
// pin both halves of the guild→org mapping: the direct-install path that
// resolves the org FROM the guild, and the org-scoped path where linkGuildToOrg
// is the only thing that touches the mapping.

// TestInstallHealsStaleGuildLink is the failing case: a guild whose mapped org
// was soft-deleted could never be re-installed, because the bare
// GetOrganization behind the link answered sql.ErrNoRows on every attempt.
func TestInstallHealsStaleGuildLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	doomed := models.NewOrganization("acme-one", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, doomed))

	staleLink := models.NewOrganizationProvider(doomed.UID, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(svc.db.CreateOrganizationProvider(ctx, staleLink))

	// The org goes; the link is not released (a deletion predating that step).
	r.NoError(svc.db.DeleteOrganization(ctx, doomed.UID))

	// A direct "Add to Server" install: no target org, so the guild→org mapping
	// is the only thing that decides where it lands.
	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(ctx, t, svc, ""))
	r.NoError(err, "a stale mapping must not brick the install")

	healed, err := svc.db.GetOrganizationBySlug(ctx, result.OrgSlug)
	r.NoError(err)
	r.NotEqual(doomed.UID, healed.UID, "the install must land in a FRESH org, not the deleted one")

	relinked, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(err)
	r.Equal(healed.UID, relinked.OrganizationUID)
	r.NotEqual(staleLink.UID, relinked.UID)

	_, err = svc.db.GetOrganizationProvider(ctx, staleLink.UID)
	r.ErrorIs(err, sql.ErrNoRows, "the stale link must be left cleared")
}

// TestInstallReusesLiveGuildLink is the positive control: with the mapped org
// alive, the same direct-install path reuses it and creates no second org.
func TestInstallReusesLiveGuildLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	home := models.NewOrganization("acme-one", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, home))

	link := models.NewOrganizationProvider(home.UID, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(svc.db.CreateOrganizationProvider(ctx, link))

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(ctx, t, svc, ""))
	r.NoError(err)
	r.Equal(home.Slug, result.OrgSlug)

	orgs, err := svc.db.ListOrganizations(ctx)
	r.NoError(err)
	r.Len(orgs, 1, "a live mapping must not mint a second org")

	unchanged, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(err)
	r.Equal(link.UID, unchanged.UID, "a live link must be left untouched")
}

// TestOrgScopedInstallRemapsStaleGuildLink covers linkGuildToOrg, which an
// org-scoped install reaches WITHOUT going through the org resolution above.
// Its "already mapped — keep the existing mapping" branch used to fire for a
// mapping that pointed at a soft-deleted org, so the guild ended up with no
// live mapping at all and the install it just completed was never recorded.
func TestOrgScopedInstallRemapsStaleGuildLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	doomed := models.NewOrganization("acme-one", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, doomed))
	r.NoError(svc.db.CreateOrganizationProvider(
		ctx, models.NewOrganizationProvider(doomed.UID, models.ProviderTypeDiscord, fake.guild.ID)))
	r.NoError(svc.db.DeleteOrganization(ctx, doomed.UID))

	installer := models.NewOrganization("acmecorp2", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, installer))

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(ctx, t, svc, installer.Slug))
	r.NoError(err)
	r.Equal(installer.Slug, result.OrgSlug)

	mapping, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(err)
	r.Equal(installer.UID, mapping.OrganizationUID,
		"a dead mapping must not keep the guild from being mapped to the installing org")
}

// TestOrgScopedInstallKeepsLiveForeignGuildLink is the positive control for the
// remap above, and the behavior the remap must not break: when the guild's home
// org is ALIVE, an install from another org leaves the mapping where it is. The
// second org still gets its own connection row — only the mapping is exclusive.
func TestOrgScopedInstallKeepsLiveForeignGuildLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	home := models.NewOrganization("acme-one", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, home))

	homeLink := models.NewOrganizationProvider(home.UID, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(svc.db.CreateOrganizationProvider(ctx, homeLink))

	other := models.NewOrganization("acmecorp2", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, other))

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(ctx, t, svc, other.Slug))
	r.NoError(err)
	r.Equal(other.Slug, result.OrgSlug)

	mapping, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(err)
	r.Equal(homeLink.UID, mapping.UID)
	r.Equal(home.UID, mapping.OrganizationUID, "a live mapping stays with the first org")

	conn, err := svc.db.GetChannelByPropertyForOrg(
		ctx, other.UID, string(models.ConnectionTypeDiscord), "guild_id", fake.guild.ID)
	r.NoError(err)
	r.NotNil(conn, "the installing org still gets its own connection")
}
