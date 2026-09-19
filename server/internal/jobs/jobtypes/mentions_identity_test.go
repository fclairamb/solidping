package jobtypes

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// addSlackContact gives a user a `slack_user` notification contact in the
// fixture org. teamID nil means "unknown workspace" — the shape every contact
// created before the column existed has.
func (f *mentionFixture) addSlackContact(
	ctx context.Context, t *testing.T, user *models.User, slackUserID string, teamID *string,
) {
	t.Helper()

	r := require.New(t)

	contact := models.NewUserContact(
		user.UID, f.org.UID, models.UserContactTypeSlackUser, slackUserID, "Slack DM")
	contact.TeamID = teamID
	r.NoError(f.dbSvc.UpsertUserContact(ctx, contact))
	r.NoError(f.dbSvc.EnsureUserNotificationRoute(ctx, user.UID, f.org.UID, contact.UID))
}

// addSlackSignIn records a Slack auth provider for the user.
func (f *mentionFixture) addSlackSignIn(
	ctx context.Context, t *testing.T, user *models.User, slackUserID string,
) {
	t.Helper()

	require.NoError(t, f.dbSvc.CreateUserProvider(ctx,
		models.NewUserProvider(user.UID, models.ProviderTypeSlack, slackUserID)))
}

// bindOrgSlackSignIn declares that the org signs in through `teamID`.
func (f *mentionFixture) bindOrgSlackSignIn(ctx context.Context, t *testing.T, teamID string) {
	t.Helper()

	require.NoError(t, f.dbSvc.CreateOrganizationProvider(ctx,
		models.NewOrganizationProvider(f.org.UID, models.ProviderTypeSlack, teamID)))
}

// mentionFor resolves the single expected mention target for the fixture's
// one-user policy.
func (f *mentionFixture) mentionFor(ctx context.Context, t *testing.T) string {
	t.Helper()

	targets := ResolveOnCallMentions(
		ctx, f.jctx, slog.Default(), f.conn, f.check, "incident.created", "")
	require.Len(t, targets, 1)

	return targets[0].ExternalID
}

// TestMentionIdentityRowBeatsDeclaredContact: the admin's mapping is the
// answer, even when the member declared a different handle themselves.
func TestMentionIdentityRowBeatsDeclaredContact(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	fx := newMentionFixture(ctx, t, "ident-adminwins", true)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	team := "T1"
	fx.addSlackContact(ctx, t, adam, "U-SELF-DECLARED", &team)
	fx.mapIdentity(ctx, t, adam, "U-ADMIN-MAPPED", "Adam")

	require.Equal(t, "U-ADMIN-MAPPED", fx.mentionFor(ctx, t))
}

// TestMentionDeclaredContactUsedOnWorkspaceMatch is the positive control for
// §2 source 2, paired with its negative control: a contact belonging to
// ANOTHER workspace is never used, because the same Slack user id there is a
// different human.
func TestMentionDeclaredContactUsedOnWorkspaceMatch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("same workspace pings", func(t *testing.T) {
		t.Parallel()

		fx := newMentionFixture(ctx, t, "ident-sameteam", true)
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

		team := "T1" // the fixture integration's team id
		fx.addSlackContact(ctx, t, adam, "U-CONTACT", &team)

		require.Equal(t, "U-CONTACT", fx.mentionFor(ctx, t))
	})

	t.Run("foreign workspace stays plain text", func(t *testing.T) {
		t.Parallel()

		fx := newMentionFixture(ctx, t, "ident-otherteam", true)
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

		other := "T-SOMEWHERE-ELSE"
		fx.addSlackContact(ctx, t, adam, "U-CONTACT", &other)

		targets := ResolveOnCallMentions(
			ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")
		require.Len(t, targets, 1)
		require.Empty(t, targets[0].ExternalID, "a foreign-workspace handle must never be pinged")
		require.Equal(t, "Adam", targets[0].DisplayName)
	})
}

// TestMentionDeclaredContactUnknownWorkspace: a contact with no team id is
// usable only when the org has exactly one Slack integration — then there is
// no other workspace it could have come from. Add a second one and it becomes
// a guess, so it is dropped.
func TestMentionDeclaredContactUnknownWorkspace(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("single slack integration", func(t *testing.T) {
		t.Parallel()

		fx := newMentionFixture(ctx, t, "ident-onlyslack", true)
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})
		fx.addSlackContact(ctx, t, adam, "U-LEGACY", nil)

		require.Equal(t, "U-LEGACY", fx.mentionFor(ctx, t))
	})

	t.Run("two slack integrations", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		fx := newMentionFixture(ctx, t, "ident-twoslack", true)
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})
		fx.addSlackContact(ctx, t, adam, "U-LEGACY", nil)

		// Positive control: it DOES resolve while there is only one workspace.
		r.Equal("U-LEGACY", fx.mentionFor(ctx, t))

		second, err := (&models.SlackSettings{TeamID: "T2", TeamName: "Other"}).ToJSONMap()
		r.NoError(err)

		other := models.NewIntegration(fx.org.UID, models.ConnectionTypeSlack, "Other Slack")
		other.Settings = second
		r.NoError(fx.dbSvc.CreateChannel(ctx, other))

		targets := ResolveOnCallMentions(
			ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")
		r.Len(targets, 1)
		r.Empty(targets[0].ExternalID,
			"with two workspaces a team-less contact is a guess, and guessing pings the wrong person")
	})
}

// TestMentionSlackSignInFallback covers §2 source 3 with both halves: the
// sign-in is used when the org's Slack sign-in is the SAME workspace as the
// integration, and ignored when it is not.
func TestMentionSlackSignInFallback(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("matching team", func(t *testing.T) {
		t.Parallel()

		fx := newMentionFixture(ctx, t, "ident-signin-ok", true)
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

		fx.bindOrgSlackSignIn(ctx, t, "T1")
		fx.addSlackSignIn(ctx, t, adam, "U-SIGNIN")

		require.Equal(t, "U-SIGNIN", fx.mentionFor(ctx, t))
	})

	t.Run("non-matching team", func(t *testing.T) {
		t.Parallel()

		fx := newMentionFixture(ctx, t, "ident-signin-bad", true)
		adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
		fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

		// The org signs in through a DIFFERENT workspace than the one the
		// integration posts into.
		fx.bindOrgSlackSignIn(ctx, t, "T-ELSEWHERE")
		fx.addSlackSignIn(ctx, t, adam, "U-SIGNIN")

		targets := ResolveOnCallMentions(
			ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")
		require.Len(t, targets, 1)
		require.Empty(t, targets[0].ExternalID)
	})
}
