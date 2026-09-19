package jobtypes

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// newDiscordMentionFixture is newMentionFixture with a bot-mode DISCORD
// integration in place of the Slack one, so the same policy/resolution plumbing
// exercises the Discord branch of declaredIdentityFor.
func newDiscordMentionFixture(
	ctx context.Context, t *testing.T, slug string,
) *mentionFixture {
	t.Helper()

	r := require.New(t)
	fx := newMentionFixture(ctx, t, slug, true)

	settings, err := (&models.DiscordSettings{
		GuildID:       "G-ACME",
		GuildName:     "Acme",
		ChannelID:     "C-ALERTS",
		MentionOnCall: true,
	}).ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(fx.org.UID, models.ConnectionTypeDiscord, "Acme Discord")
	conn.Settings = settings
	r.NoError(fx.dbSvc.CreateChannel(ctx, conn))

	fx.conn = conn

	return fx
}

// addDiscordContact gives a user a `discord` notification contact in the org.
func (f *mentionFixture) addDiscordContact(
	ctx context.Context, t *testing.T, user *models.User, snowflake string,
) {
	t.Helper()

	r := require.New(t)

	contact := models.NewUserContact(
		user.UID, f.org.UID, models.UserContactTypeDiscord, snowflake, "Discord")
	r.NoError(f.dbSvc.UpsertUserContact(ctx, contact))
	r.NoError(f.dbSvc.EnsureUserNotificationRoute(ctx, user.UID, f.org.UID, contact.UID))
}

// addDiscordSignIn records a Discord auth provider for the user.
func (f *mentionFixture) addDiscordSignIn(
	ctx context.Context, t *testing.T, user *models.User, snowflake string,
) {
	t.Helper()

	require.NoError(t, f.dbSvc.CreateUserProvider(ctx,
		models.NewUserProvider(user.UID, models.ProviderTypeDiscord, snowflake)))
}

// TestDiscordMentionAdminMappingBeatsDeclaredIdentity is the precedence assertion
// the spec asks for, WITH its positive control: the declared identity does resolve
// on its own, and stops being used the moment an admin says otherwise.
//
// The order is what makes "an admin mapping always wins" true — a member who
// declared the wrong snowflake cannot override the admin's correction.
func TestDiscordMentionAdminMappingBeatsDeclaredIdentity(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("positive control: the declared contact resolves on its own", func(t *testing.T) {
		t.Parallel()

		fx := newDiscordMentionFixture(ctx, t, "dmention-declared")
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})
		fx.addDiscordContact(ctx, t, adam, "SNOW-SELF-DECLARED")

		require.Equal(t, "SNOW-SELF-DECLARED", fx.mentionFor(ctx, t))
	})

	t.Run("the admin mapping wins", func(t *testing.T) {
		t.Parallel()

		fx := newDiscordMentionFixture(ctx, t, "dmention-adminwins")
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})
		fx.addDiscordContact(ctx, t, adam, "SNOW-SELF-DECLARED")
		fx.mapIdentity(ctx, t, adam, "SNOW-ADMIN-MAPPED", "Adam")

		require.Equal(t, "SNOW-ADMIN-MAPPED", fx.mentionFor(ctx, t))
	})
}

// TestDiscordMentionSignInFallback: a member who only ever pressed "Sign in with
// Discord" is now mentioned for free. Before this spec the sender ignored that
// row entirely — the admin mapping table resolved it, and the sender did not,
// which is precisely the disagreement identitylink exists to end.
func TestDiscordMentionSignInFallback(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	fx := newDiscordMentionFixture(ctx, t, "dmention-signin")
	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})
	fx.addDiscordSignIn(ctx, t, adam, "SNOW-SIGNIN")

	require.Equal(t, "SNOW-SIGNIN", fx.mentionFor(ctx, t))
}

// TestDiscordMentionUnlinkedStaysPlainText is the deliberate degradation: a member
// with nothing linking them to Discord is NAMED but pings nobody.
func TestDiscordMentionUnlinkedStaysPlainText(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	fx := newDiscordMentionFixture(ctx, t, "dmention-unlinked")
	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")
	r.Len(targets, 1)
	r.Empty(targets[0].ExternalID)
	r.Equal("Adam", targets[0].DisplayName)
}

// TestDiscordMentionIgnoresSlackDeclarations: a `slack_user` contact must never be
// rendered as a Discord mention. The two id spaces are unrelated, and `<@U123>`
// in Discord addresses nobody at best.
func TestDiscordMentionIgnoresSlackDeclarations(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	fx := newDiscordMentionFixture(ctx, t, "dmention-slackonly")
	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	team := "T1"
	fx.addSlackContact(ctx, t, adam, "U-SLACK", &team)
	fx.addSlackSignIn(ctx, t, adam, "U-SLACK")

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")
	r.Len(targets, 1)
	r.Empty(targets[0].ExternalID, "a Slack handle must never become a Discord mention")
}
