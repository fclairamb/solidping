package usernotifications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestListRoutes_SlackAndDiscordMentionBuilders(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)

	// No integration yet: no mention, no suggestion, no team id.
	resp, err := env.svc.ListRoutes(ctx, env.org.Slug, env.user)
	r.NoError(err)
	r.Nil(resp.SlackMention)
	r.Nil(resp.DiscordMention)
	r.Nil(env.svc.slackTeamIDForOrg(ctx, env.org.UID))

	slackSettings, err := (&models.SlackSettings{TeamID: "T1", TeamName: "Acme WS", ChannelID: "C1"}).ToJSONMap()
	r.NoError(err)
	slack := models.NewIntegration(env.org.UID, models.ConnectionTypeSlack, "slack")
	slack.Settings = slackSettings
	r.NoError(env.db.CreateChannel(ctx, slack))

	discordSettings, err := (&models.DiscordSettings{
		GuildID: "G1", GuildName: "Acme Guild", ChannelID: "D1",
	}).ToJSONMap()
	r.NoError(err)
	discord := models.NewIntegration(env.org.UID, models.ConnectionTypeDiscord, "discord")
	discord.Settings = discordSettings
	r.NoError(env.db.CreateChannel(ctx, discord))

	// A webhook-mode Discord integration must be skipped.
	hookSettings, err := (&models.DiscordSettings{WebhookURL: "https://discord.com/api/webhooks/1/x"}).ToJSONMap()
	r.NoError(err)
	hook := models.NewIntegration(env.org.UID, models.ConnectionTypeDiscord, "hook")
	hook.Settings = hookSettings
	r.NoError(env.db.CreateChannel(ctx, hook))

	teamID := env.svc.slackTeamIDForOrg(ctx, env.org.UID)
	r.NotNil(teamID)
	r.Equal("T1", *teamID)

	r.NoError(env.db.CreateUserProvider(ctx, models.NewUserProvider(env.user.UID, models.ProviderTypeSlack, "U123")))
	r.NoError(env.db.CreateUserProvider(ctx, models.NewUserProvider(env.user.UID, models.ProviderTypeDiscord, "555")))

	resp, err = env.svc.ListRoutes(ctx, env.org.Slug, env.user)
	r.NoError(err)
	r.NotNil(resp.SlackSuggestion)
	r.Equal("U123", resp.SlackSuggestion.SlackUserID)
	r.Equal("Acme WS", resp.SlackSuggestion.WorkspaceName)
	r.NotNil(resp.SlackMention)
	r.Equal("Acme WS", resp.SlackMention.Workspace)
	r.NotNil(resp.DiscordMention)
	r.Equal("Acme Guild", resp.DiscordMention.Guild)

	// An admin-mapped identity wins and shows as linked.
	r.NoError(env.db.UpsertUserIntegrationIdentity(ctx, &models.UserIntegrationIdentity{
		UID: "11111111-1111-1111-1111-111111111111", OrganizationUID: env.org.UID,
		IntegrationUID: slack.UID, UserUID: env.user.UID, ExternalID: "U999", Source: "manual",
	}))
	r.NoError(env.db.UpsertUserIntegrationIdentity(ctx, &models.UserIntegrationIdentity{
		UID: "22222222-2222-2222-2222-222222222222", OrganizationUID: env.org.UID,
		IntegrationUID: discord.UID, UserUID: env.user.UID, ExternalID: "777", Source: "manual",
	}))

	resp, err = env.svc.ListRoutes(ctx, env.org.Slug, env.user)
	r.NoError(err)
	r.True(resp.SlackMention.Linked)
	r.Equal("U999", resp.SlackMention.ExternalID)
	r.True(resp.DiscordMention.Linked)
	r.Equal("777", resp.DiscordMention.ExternalID)
}
