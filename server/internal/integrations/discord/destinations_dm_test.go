package discord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// dmDestEnv is an org with a bot-mode Discord integration and two members: one
// with a Discord identity, one without.
type dmDestEnv struct {
	svc      *Service
	fake     *fakeDiscord
	org      *models.Organization
	conn     *models.Integration
	linked   *models.User
	unlinked *models.User
}

func setupDMDestEnv(t *testing.T) (context.Context, *dmDestEnv) {
	t.Helper()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	org := models.NewOrganization("acme", "ACME")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	linked := models.NewUser("alice@acme.com")
	linked.Name = "Alice"
	r.NoError(svc.db.CreateUser(ctx, linked))
	r.NoError(svc.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, linked.UID, models.MemberRoleAdmin)))

	unlinked := models.NewUser("bob@acme.com")
	unlinked.Name = "Bob"
	r.NoError(svc.db.CreateUser(ctx, unlinked))
	r.NoError(svc.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, unlinked.UID, models.MemberRoleUser)))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeDiscord, "acme discord")
	settings, err := (&models.DiscordSettings{
		GuildID:   "G-ACME",
		GuildName: "acme",
		ChannelID: "C-ALERTS",
	}).ToJSONMap()
	r.NoError(err)
	conn.Settings = settings
	r.NoError(svc.db.CreateChannel(ctx, conn))

	return ctx, &dmDestEnv{
		svc: svc, fake: fake, org: org, conn: conn, linked: linked, unlinked: unlinked,
	}
}

// TestGetDestinationsListsOnlyIdentityResolvedUsers: the DM tab is the org's
// members with a RESOLVED Discord identity — never the guild member list, which
// would need the privileged GUILD_MEMBERS intent and would not say which
// SolidPing account any guild member is.
func TestGetDestinationsListsOnlyIdentityResolvedUsers(t *testing.T) {
	r := require.New(t)
	ctx, env := setupDMDestEnv(t)

	// Alice declared her own Discord account with a `discord` contact.
	contact := models.NewUserContact(
		env.linked.UID, env.org.UID, models.UserContactTypeDiscord, "SNOW-ALICE", "Discord")
	r.NoError(env.svc.db.UpsertUserContact(ctx, contact))
	r.NoError(env.svc.db.EnsureUserNotificationRoute(ctx, env.linked.UID, env.org.UID, contact.UID))

	resp, err := env.svc.GetDestinations(ctx, env.org.Slug, env.conn.UID)
	r.NoError(err)

	r.Len(resp.Users, 1, "Bob has nothing linking him to Discord and must not be offered")
	r.Equal("SNOW-ALICE", resp.Users[0].ID)
	r.Equal("Alice", resp.Users[0].Name)
	r.Equal(env.linked.UID, resp.Users[0].UserUID)

	// The channels half is unchanged: voice and category rows stay filtered out.
	r.Len(resp.Channels, 2)
}

// TestGetDestinationsPrefersAdminMappingOverDeclaredIdentity: the picker must
// show who the SENDER will actually address, and the admin's mapping is what the
// sender prefers.
func TestGetDestinationsPrefersAdminMappingOverDeclaredIdentity(t *testing.T) {
	r := require.New(t)
	ctx, env := setupDMDestEnv(t)

	contact := models.NewUserContact(
		env.linked.UID, env.org.UID, models.UserContactTypeDiscord, "SNOW-DECLARED", "Discord")
	r.NoError(env.svc.db.UpsertUserContact(ctx, contact))
	r.NoError(env.svc.db.EnsureUserNotificationRoute(ctx, env.linked.UID, env.org.UID, contact.UID))

	identity := models.NewUserIntegrationIdentity(
		env.org.UID, env.conn.UID, env.linked.UID, "SNOW-ADMIN", "Alice",
		models.IdentitySourceManual)
	r.NoError(env.svc.db.UpsertUserIntegrationIdentity(ctx, identity))

	resp, err := env.svc.GetDestinations(ctx, env.org.Slug, env.conn.UID)
	r.NoError(err)
	r.Len(resp.Users, 1)
	r.Equal("SNOW-ADMIN", resp.Users[0].ID)
}

// TestOpenDMDestinationOpensTheChannel: picking a user opens the DM AT PICK TIME
// and returns the channel id the integration will store, so an admin finds out
// immediately whether the destination works.
func TestOpenDMDestinationOpensTheChannel(t *testing.T) {
	r := require.New(t)
	ctx, env := setupDMDestEnv(t)

	contact := models.NewUserContact(
		env.linked.UID, env.org.UID, models.UserContactTypeDiscord, "SNOW-ALICE", "Discord")
	r.NoError(env.svc.db.UpsertUserContact(ctx, contact))
	r.NoError(env.svc.db.EnsureUserNotificationRoute(ctx, env.linked.UID, env.org.UID, contact.UID))

	resp, err := env.svc.OpenDMDestination(ctx, env.org.Slug, env.conn.UID, "SNOW-ALICE")
	r.NoError(err)
	r.Equal("DM-SNOW-ALICE", resp.ChannelID)
	r.Equal("SNOW-ALICE", resp.UserID)
	r.Equal("Alice", resp.Name)
	r.Equal([]string{"SNOW-ALICE"}, env.fake.dmRecipients())
}

// TestOpenDMDestinationRejectsUnresolvedSnowflake is the same security rule the
// `discord` contact type enforces, at the admin end: an arbitrary snowflake must
// never become a destination, or an admin could route a check's alerts to any
// Discord account on earth.
func TestOpenDMDestinationRejectsUnresolvedSnowflake(t *testing.T) {
	r := require.New(t)
	ctx, env := setupDMDestEnv(t)

	_, err := env.svc.OpenDMDestination(ctx, env.org.Slug, env.conn.UID, "SNOW-STRANGER")
	r.ErrorIs(err, ErrDiscordUserNotResolved)
	r.Empty(env.fake.dmRecipients(), "nothing may reach Discord for an unresolved user")
}
