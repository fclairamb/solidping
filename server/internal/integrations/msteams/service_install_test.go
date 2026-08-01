package msteams

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestHandleInstall_CapturesConversationAndDefaults covers the spec's
// "install event → connection + destination creation" criterion: the
// installationUpdate carries the tenant and the conversation reference, and
// the first conversation becomes the default notification destination.
func TestHandleInstall_CapturesConversationAndDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-install", testTenantID)

	updated, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)
	r.Equal(conn.UID, updated.UID)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(updated.Settings)
	r.NoError(err)

	r.Equal(testTenantID, settings.TenantID)
	r.Equal("28:"+testAppID, settings.BotID)
	r.Equal("29:installer", settings.InstalledByUserID)
	// The trailing slash Microsoft sends must be normalized away, or every
	// connector URL we build would double-slash.
	r.Equal("https://smba.trafficmanager.net/emea", settings.ServiceURL)

	r.Len(settings.Destinations, 1)
	r.Equal("19:channel-a", settings.Destinations[0].ID)
	r.Equal("alerts", settings.Destinations[0].Name)
	r.Equal("19:team", settings.Destinations[0].TeamID)
	r.Equal(DestinationTypeChannel, settings.Destinations[0].Type)

	// First conversation becomes the default destination.
	r.Equal("19:channel-a", settings.ChannelID)
	r.Equal("alerts", settings.ChannelName)
}

// TestHandleInstall_SecondChannelDoesNotStealDefault pins that adding the bot
// to another channel adds a destination but leaves the configured default
// alone.
func TestHandleInstall_SecondChannelDoesNotStealDefault(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-install-2", testTenantID)

	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)

	second := installActivity(testTenantID, "19:channel-b", InstallActionAdd)
	second.ChannelData.Channel.Name = "incidents"

	updated, err := svc.HandleInstall(ctx, second)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(updated.Settings)
	r.NoError(err)

	r.Len(settings.Destinations, 2)
	r.Equal("19:channel-a", settings.ChannelID, "the default must not move on a later install")
}

// TestHandleInstall_IsIdempotent guards against Bot Framework redelivery
// duplicating a destination.
func TestHandleInstall_IsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-install-3", testTenantID)

	for range 3 {
		_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
		r.NoError(err)
	}

	conn, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Len(settings.Destinations, 1)
}

// TestHandleInstall_UnknownTenantIsRefused is the security-relevant negative
// control: an install from a tenant nobody linked must NOT silently attach
// itself to some organization.
func TestHandleInstall_UnknownTenantIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	// An org exists, but it never claimed this tenant.
	newConnection(ctx, t, svc, "teams-other", "some-other-tenant")

	_, err := svc.HandleInstall(ctx, installActivity("unclaimed-tenant", "19:channel-a", InstallActionAdd))
	r.ErrorIs(err, ErrTenantNotLinked)
}

// TestHandleInstall_AutoLinksPinnedSingleOrgInstance covers the self-hosted
// convenience path: tenant pinned in config AND exactly one org on the
// instance.
func TestHandleInstall_AutoLinksPinnedSingleOrgInstance(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	svc.cfg.MSTeams.TenantID = testTenantID

	org := models.NewOrganization("teams-solo", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	conn, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)
	r.Equal(org.UID, conn.OrganizationUID)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal(testTenantID, settings.TenantID)
	r.Len(settings.Destinations, 1)
}

// TestHandleInstall_DoesNotAutoLinkWithoutPinnedTenant is the counterpart
// negative control: without the tenant pin, a single-org instance must not
// adopt a random tenant that sideloaded the manifest.
func TestHandleInstall_DoesNotAutoLinkWithoutPinnedTenant(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	org := models.NewOrganization("teams-solo-2", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	_, err := svc.HandleInstall(ctx, installActivity("random-tenant", "19:channel-a", InstallActionAdd))
	r.ErrorIs(err, ErrTenantNotLinked)
}

// TestHandleInstall_DoesNotAutoLinkMultiOrgInstance pins the other half of
// the auto-link guard.
func TestHandleInstall_DoesNotAutoLinkMultiOrgInstance(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	svc.cfg.MSTeams.TenantID = testTenantID

	for _, slug := range []string{"teams-multi-a", "teams-multi-b"} {
		org := models.NewOrganization(slug, "")
		r.NoError(svc.db.CreateOrganization(ctx, org))
	}

	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.ErrorIs(err, ErrTenantNotLinked)
}

// TestHandleInstall_MissingTenantIsRefused covers an activity with no tenant
// identity at all.
func TestHandleInstall_MissingTenantIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	activity := installActivity(testTenantID, "19:channel-a", InstallActionAdd)
	activity.ChannelData.Tenant = nil
	activity.Conversation.TenantID = ""

	_, err := svc.HandleInstall(ctx, activity)
	r.ErrorIs(err, ErrNoTenantID)
}

// TestDispatchActivity_ConversationUpdateRegistersChannel covers the
// conversationUpdate branch: the bot being added to a channel must register
// that channel as a destination and greet it.
func TestDispatchActivity_ConversationUpdateRegistersChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(ctx, t, svc, "teams-convupdate", testTenantID)

	activity := installActivity(testTenantID, "19:channel-a", "")
	activity.Type = ActivityTypeConversationUpdate
	activity.MembersAdded = []ChannelAccount{{ID: "28:" + testAppID}}

	r.NoError(DispatchActivity(ctx, svc, activity))

	conn, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Len(settings.Destinations, 1)

	r.True(connector.containsText("SolidPing is ready"), "the bot should greet the channel it joined")
}

// TestDispatchActivity_ConversationUpdateIgnoresOtherMembers is the negative
// control: a human joining the channel must not be mistaken for an install.
func TestDispatchActivity_ConversationUpdateIgnoresOtherMembers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(ctx, t, svc, "teams-convupdate-2", testTenantID)

	activity := installActivity(testTenantID, "19:channel-a", "")
	activity.Type = ActivityTypeConversationUpdate
	activity.MembersAdded = []ChannelAccount{{ID: "29:some-human"}}

	r.NoError(DispatchActivity(ctx, svc, activity))

	conn, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Empty(settings.Destinations)
	r.Empty(connector.recorded())
}

// TestDispatchActivity_UnlinkedTenantGetsActionableReply pins the
// self-service onboarding path: an install from an unlinked tenant answers in
// the channel with the tenant id the admin must paste into the dashboard.
func TestDispatchActivity_UnlinkedTenantGetsActionableReply(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	r.NoError(DispatchActivity(ctx, svc, installActivity("unlinked-tenant", "19:channel-a", InstallActionAdd)))

	calls := connector.recorded()
	r.Len(calls, 1)

	card, ok := calls[0].Activity.Attachments[0].Content.(map[string]any)
	r.True(ok)

	body, err := json.Marshal(card)
	r.NoError(err)
	r.Contains(string(body), "unlinked-tenant")
}
