package msteams

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// cardJSON renders the Adaptive Card of a recorded call back to JSON so
// assertions can look for text anywhere in the card body.
func cardJSON(t *testing.T, call recordedCall) string {
	t.Helper()

	require.NotEmpty(t, call.Activity.Attachments, "activity carries no card")

	raw, err := json.Marshal(call.Activity.Attachments[0].Content)
	require.NoError(t, err)

	return string(raw)
}

// TestDispatchActivity_MentionRepliesWithHelp covers the end-to-end mention
// path: an @mention is stripped, parsed, and answered as a threaded reply to
// the original message.
func TestDispatchActivity_MentionRepliesWithHelp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(t, ctx, svc, "teams-route-help", testTenantID)

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> help")))

	calls := connector.recorded()
	r.Len(calls, 1)
	// Replying to the inbound activity id is what threads the answer under
	// the user's message in the Teams channel.
	r.Equal("/v3/conversations/19:channel-a/activities/inbound-1", calls[0].Path)
	r.Contains(cardJSON(t, calls[0]), "checks add")
}

// TestDispatchActivity_UnknownCommandWarns pins the error path.
func TestDispatchActivity_UnknownCommandWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(t, ctx, svc, "teams-route-bad", testTenantID)

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> frobnicate")))

	r.True(connector.containsText("Unknown command"))
}

// TestDispatchActivity_IgnoresOwnMessages is the loop guard: the bot's own
// replies must never re-enter the parser.
func TestDispatchActivity_IgnoresOwnMessages(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(t, ctx, svc, "teams-route-self", testTenantID)

	activity := messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> help")
	activity.From = &ChannelAccount{ID: "28:" + testAppID}

	r.NoError(DispatchActivity(ctx, svc, activity))
	r.Empty(connector.recorded())
}

// TestDispatchActivity_ConfigDefaultChannelUsesCurrentConversation resolves
// the spec's second open question: Teams has no cross-team channel-reference
// syntax, so `config default-channel` is scoped to the team the command came
// from and defaults to the current conversation.
func TestDispatchActivity_ConfigDefaultChannelUsesCurrentConversation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(t, ctx, svc, "teams-route-cfg", testTenantID)

	// Two channels captured; the first is the default.
	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)

	second := installActivity(testTenantID, "19:channel-b", InstallActionAdd)
	second.ChannelData.Channel.Name = "incidents"
	_, err = svc.HandleInstall(ctx, second)
	r.NoError(err)

	// Issue the command from channel-b — it must become the default.
	activity := messageActivity(testTenantID, "19:channel-b", "<at>SolidPing</at> config default-channel")
	activity.ChannelData.Channel = &TeamsChannelInfo{ID: "19:channel-b", Name: "incidents"}

	r.NoError(DispatchActivity(ctx, svc, activity))

	conn, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal("19:channel-b", settings.ChannelID)

	r.True(connector.containsText("Default channel updated"))
}

// TestDispatchActivity_UnlinkedTenantMessageIsActionable pins that a command
// from an unlinked tenant answers with the tenant id to paste, not a generic
// failure.
func TestDispatchActivity_UnlinkedTenantMessageIsActionable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity("stranger-tenant", "19:channel-a", "<at>SolidPing</at> incidents list")))

	calls := connector.recorded()
	r.Len(calls, 1)
	r.Contains(cardJSON(t, calls[0]), "stranger-tenant")
}

// TestDispatchActivity_UninstalledTenantIsRefused checks that commands stop
// working once the app is removed, even though the row survives.
func TestDispatchActivity_UninstalledTenantIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(t, ctx, svc, "teams-route-gone", testTenantID)

	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)
	r.NoError(svc.HandleUninstall(ctx, testTenantID))

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> incidents list")))

	r.True(connector.containsText("uninstalled"))
}

// TestDispatchActivity_IncidentsListEmpty exercises a real command against
// the database, proving the tenant → org → data path works end to end.
func TestDispatchActivity_IncidentsListEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(t, ctx, svc, "teams-route-inc", testTenantID)

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> incidents list")))

	r.True(connector.containsText("No incidents found"))
}

// TestDispatchActivity_UnknownActivityTypeIsIgnored keeps unhandled Bot
// Framework activity types (typing indicators, reactions, …) inert.
func TestDispatchActivity_UnknownActivityTypeIsIgnored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(t, ctx, svc, "teams-route-typing", testTenantID)

	activity := messageActivity(testTenantID, "19:channel-a", "")
	activity.Type = "typing"

	r.NoError(DispatchActivity(ctx, svc, activity))
	r.Empty(connector.recorded())
}

// TestGetConnectionByTenantID_PicksOldestOnAmbiguity documents the routing
// tie-break when one tenant is connected to several orgs.
func TestGetConnectionByTenantID_PicksOldestOnAmbiguity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	first := newConnection(t, ctx, svc, "teams-amb-a", testTenantID)
	newConnection(t, ctx, svc, "teams-amb-b", testTenantID)

	conn, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)
	r.Equal(first.UID, conn.UID)
}
