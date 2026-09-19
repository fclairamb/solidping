package identitylink_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/identitylink"
)

// identityEnv is an org with a bot-mode Discord integration and one member.
type identityEnv struct {
	db   db.Service
	org  *models.Organization
	conn *models.Integration
	user *models.User
}

func newIdentityEnv(t *testing.T) *identityEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "ACME")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	user.Name = "Alice"
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeDiscord, "acme discord")
	settings, err := (&models.DiscordSettings{
		GuildID: "G-ACME", GuildName: "acme", ChannelID: "C-ALERTS",
	}).ToJSONMap()
	r.NoError(err)
	conn.Settings = settings
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	return &identityEnv{db: dbSvc, org: org, conn: conn, user: user}
}

func (e *identityEnv) addDiscordContact(t *testing.T, snowflake string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	contact := models.NewUserContact(
		e.user.UID, e.org.UID, models.UserContactTypeDiscord, snowflake, "Discord")
	r.NoError(e.db.UpsertUserContact(ctx, contact))
	r.NoError(e.db.EnsureUserNotificationRoute(ctx, e.user.UID, e.org.UID, contact.UID))
}

func (e *identityEnv) addDiscordSignIn(t *testing.T, snowflake string) {
	t.Helper()

	require.NoError(t, e.db.CreateUserProvider(t.Context(),
		models.NewUserProvider(e.user.UID, models.ProviderTypeDiscord, snowflake)))
}

// TestDeclaredDiscordIdentityResolutionOrder: the contact wins over the sign-in,
// because the contact is the more recent and more deliberate statement of "this
// is my Discord account, page me there".
func TestDeclaredDiscordIdentityResolutionOrder(t *testing.T) {
	t.Parallel()

	t.Run("contact only", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newIdentityEnv(t)
		env.addDiscordContact(t, "SNOW-CONTACT")

		got := identitylink.DeclaredDiscordIdentity(t.Context(), env.db, env.conn, env.user.UID)
		r.NotNil(got)
		r.Equal("SNOW-CONTACT", got.ExternalID)
		r.Equal(identitylink.SourceContact, got.Source)
	})

	t.Run("sign-in only", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newIdentityEnv(t)
		env.addDiscordSignIn(t, "SNOW-SIGNIN")

		got := identitylink.DeclaredDiscordIdentity(t.Context(), env.db, env.conn, env.user.UID)
		r.NotNil(got)
		r.Equal("SNOW-SIGNIN", got.ExternalID)
		r.Equal(identitylink.SourceAuthProvider, got.Source)
	})

	t.Run("contact outranks sign-in", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newIdentityEnv(t)
		env.addDiscordSignIn(t, "SNOW-SIGNIN")
		env.addDiscordContact(t, "SNOW-CONTACT")

		got := identitylink.DeclaredDiscordIdentity(t.Context(), env.db, env.conn, env.user.UID)
		r.NotNil(got)
		r.Equal("SNOW-CONTACT", got.ExternalID)
		r.Equal(identitylink.SourceContact, got.Source)
	})

	t.Run("nothing declared", func(t *testing.T) {
		t.Parallel()

		env := newIdentityEnv(t)
		require.Nil(t,
			identitylink.DeclaredDiscordIdentity(t.Context(), env.db, env.conn, env.user.UID))
	})
}

// TestDeclaredDiscordIdentityIsNotGuildScoped is the deliberate divergence from
// the Slack resolver, asserted so nobody "fixes" it back.
//
// A Discord snowflake is GLOBAL: the same id addresses the same human in every
// server, so there is no cross-workspace mistake to guard against. An integration
// with no guild id at all still resolves, where the Slack twin would refuse — and
// that is correct, not a hole. The cost is stated in the doc comment: a mention
// only PINGS a member who is actually in the guild; for anyone else Discord
// renders `<@id>` as inert text.
func TestDeclaredDiscordIdentityIsNotGuildScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	env := newIdentityEnv(t)
	env.addDiscordContact(t, "SNOW-CONTACT")

	// Strip the guild id: a Slack-style resolver would now return nil.
	settings, err := (&models.DiscordSettings{ChannelID: "C-ALERTS"}).ToJSONMap()
	r.NoError(err)

	guildless := models.NewIntegration(
		env.org.UID, models.ConnectionTypeDiscord, "acme discord guildless")
	guildless.Settings = settings
	r.NoError(env.db.CreateChannel(ctx, guildless))

	got := identitylink.DeclaredDiscordIdentity(ctx, env.db, guildless, env.user.UID)
	r.NotNil(got, "a snowflake is global: no guild id is needed to trust it")
	r.Equal("SNOW-CONTACT", got.ExternalID)
}

// TestDeclaredDiscordIdentityIgnoresNonDiscordIntegrations: handed a Slack
// integration it must return nil, or the two resolvers would each answer for the
// other's provider.
func TestDeclaredDiscordIdentityIgnoresNonDiscordIntegrations(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	env := newIdentityEnv(t)
	env.addDiscordContact(t, "SNOW-CONTACT")

	slackConn := models.NewIntegration(env.org.UID, models.ConnectionTypeSlack, "acme slack")
	r.NoError(env.db.CreateChannel(ctx, slackConn))

	r.Nil(identitylink.DeclaredDiscordIdentity(ctx, env.db, slackConn, env.user.UID))
}

// TestDeclaredDiscordIdentityIsNilSafe: a mention is a nicety and must never break
// a send, so every doubtful input yields nil rather than a panic.
func TestDeclaredDiscordIdentityIsNilSafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	env := newIdentityEnv(t)

	r.Nil(identitylink.DeclaredDiscordIdentity(ctx, nil, env.conn, env.user.UID))
	r.Nil(identitylink.DeclaredDiscordIdentity(ctx, env.db, nil, env.user.UID))
	r.Nil(identitylink.DeclaredDiscordIdentity(ctx, env.db, env.conn, ""))
}

// TestDeclaredDiscordIdentityIgnoresAnotherOrgsContact: contacts are org-scoped,
// and a contact added in org B says nothing about who this member is for org A's
// integration.
func TestDeclaredDiscordIdentityIgnoresAnotherOrgsContact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	env := newIdentityEnv(t)

	other := models.NewOrganization("acme-eu", "ACME EU")
	r.NoError(env.db.CreateOrganization(ctx, other))
	r.NoError(env.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(other.UID, env.user.UID, models.MemberRoleAdmin)))

	contact := models.NewUserContact(
		env.user.UID, other.UID, models.UserContactTypeDiscord, "SNOW-OTHER-ORG", "Discord")
	r.NoError(env.db.UpsertUserContact(ctx, contact))
	r.NoError(env.db.EnsureUserNotificationRoute(ctx, env.user.UID, other.UID, contact.UID))

	r.Nil(identitylink.DeclaredDiscordIdentity(ctx, env.db, env.conn, env.user.UID))
}
