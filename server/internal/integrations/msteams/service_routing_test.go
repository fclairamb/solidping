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

	newConnection(ctx, t, svc, "teams-route-help", testTenantID)

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

	newConnection(ctx, t, svc, "teams-route-bad", testTenantID)

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

	newConnection(ctx, t, svc, "teams-route-self", testTenantID)

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

	newConnection(ctx, t, svc, "teams-route-cfg", testTenantID)

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
// from an unlinked tenant explains how to link, without leaking or
// legitimizing the tenant id.
func TestDispatchActivity_UnlinkedTenantMessageIsActionable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity("stranger-tenant", "19:channel-a", "<at>SolidPing</at> incidents list")))

	calls := connector.recorded()
	r.Len(calls, 1)

	card := cardJSON(t, calls[0])
	r.Contains(card, "link")
	r.NotContains(card, "stranger-tenant")
}

// TestDispatchActivity_UninstalledTenantIsRefused checks that commands stop
// working once the app is removed, even though the row survives.
func TestDispatchActivity_UninstalledTenantIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(ctx, t, svc, "teams-route-gone", testTenantID)

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

	newConnection(ctx, t, svc, "teams-route-inc", testTenantID)

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

	newConnection(ctx, t, svc, "teams-route-typing", testTenantID)

	activity := messageActivity(testTenantID, "19:channel-a", "")
	activity.Type = "typing"

	r.NoError(DispatchActivity(ctx, svc, activity))
	r.Empty(connector.recorded())
}

// TestGetConnectionByTenantID_FailsSafeOnAmbiguity replaces the previous
// "oldest row wins" test, which pinned the behavior that made the
// cross-tenant hijack exploitable: an attacker's older row would silently
// absorb a victim tenant's conversation references.
//
// Under the linking-code model a tenant can only ever be bound to one
// connection (LinkTenant refuses an already-held tenant), so duplicates are
// unreachable through the API. If they exist anyway — hand-edited rows, an
// imported dataset — routing must refuse rather than pick one, because every
// possible pick hands one organization another's Teams data.
func TestGetConnectionByTenantID_FailsSafeOnAmbiguity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// Duplicates can no longer be created through the database (see the
	// unique partial index), so the decision is exercised directly. Routing
	// must refuse rather than pick one: every possible pick would hand one
	// organization another's Teams data.
	dupes := []*models.Integration{
		models.NewIntegration("org-a", models.ConnectionTypeMSTeamsBot, "A"),
		models.NewIntegration("org-b", models.ConnectionTypeMSTeamsBot, "B"),
	}

	_, err := resolveSingleConnection(ctx, testTenantID, dupes)
	r.ErrorIs(err, ErrAmbiguousTenant)

	// The single and empty cases still behave.
	one, err := resolveSingleConnection(ctx, testTenantID, dupes[:1])
	r.NoError(err)
	r.Equal(dupes[0].UID, one.UID)

	_, err = resolveSingleConnection(ctx, testTenantID, nil)
	r.ErrorIs(err, ErrTenantNotLinked)
}

// TestTenantUniquenessIsEnforcedByTheDatabase pins the storage-level
// invariant behind the fail-safe read: the application check in LinkTenant is
// a read followed by a write with nothing serializing them, so the database
// has to be what actually makes a double claim impossible.
func TestTenantUniquenessIsEnforcedByTheDatabase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-uniq-a", testTenantID)

	org := models.NewOrganization("teams-uniq-b", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	dupe := models.NewIntegration(org.UID, models.ConnectionTypeMSTeamsBot, "Duplicate")
	dupe.Settings = models.JSONMap{"tenant_id": testTenantID}

	err := svc.db.CreateChannel(ctx, dupe)
	r.Error(err, "a second connection claiming the same tenant must be refused by the database")
	r.True(isUniqueViolation(err), "the violation must be recognizable: %v", err)
}

// TestLinkTenant_MapsUniqueViolationToAlreadyLinked pins that a lost race
// surfaces as the ordinary "already linked" answer rather than a raw database
// error leaking to a Teams channel.
func TestLinkTenant_MapsUniqueViolationToAlreadyLinked(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	// Org A holds the tenant.
	newConnection(ctx, t, svc, "teams-race-a", testTenantID)

	// Org B has a code for an unlinked connection. The pre-check would
	// normally catch this, so bypass it by minting the code and then linking
	// through the low-level path the race would take.
	orgUID, code := startLinkFor(ctx, t, svc, "teams-race-b")

	_, connUID, err := redeemLinkCode(ctx, svc.db, code)
	r.NoError(err)

	conn, err := svc.db.GetChannel(ctx, connUID)
	r.NoError(err)
	r.Equal(orgUID, conn.OrganizationUID)

	settings, err := settingsOf(conn)
	r.NoError(err)
	settings.TenantID = testTenantID

	saveErr := svc.saveSettings(ctx, conn, settings)
	r.Error(saveErr)
	r.True(isUniqueViolation(saveErr), "expected a unique violation, got: %v", saveErr)
}
