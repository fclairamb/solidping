package msteams

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestGetDestinations_SortsByTeamThenChannel pins the picker ordering: Teams
// delivers conversationUpdate activities in arbitrary order, so the stored
// list is unsorted and the API must sort it.
func TestGetDestinations_SortsByTeamThenChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	org := models.NewOrganization("teams-dest-sort", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	settings := &models.MSTeamsBotSettings{
		TenantID: testTenantID,
		Destinations: []models.MSTeamsDestination{
			{ID: "1", Name: "zulu", TeamName: "Ops"},
			{ID: "2", Name: "Alpha", TeamName: "Ops"},
			{ID: "3", Name: "general", TeamName: "engineering"},
		},
	}

	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeMSTeamsBot, "Microsoft Teams")
	conn.Settings = settingsMap
	r.NoError(svc.db.CreateChannel(ctx, conn))

	resp, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.NoError(err)

	ids := make([]string, 0, len(resp.Destinations))
	for _, dest := range resp.Destinations {
		ids = append(ids, dest.ID)
	}

	// engineering < Ops (case-insensitive), then Alpha < zulu within Ops.
	r.Equal([]string{"3", "2", "1"}, ids)
	r.True(resp.Connected)
	r.False(resp.Uninstalled)
	r.Equal(testTenantID, resp.TenantID)
}

// TestGetDestinations_ReportsUninstalledState lets the dashboard explain a
// dead connection instead of showing an empty picker.
func TestGetDestinations_ReportsUninstalledState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-dest-uninst", testTenantID)

	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)
	r.NoError(svc.HandleUninstall(ctx, testTenantID))

	org, err := svc.db.GetOrganization(ctx, conn.OrganizationUID)
	r.NoError(err)

	resp, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.NoError(err)
	r.True(resp.Uninstalled)
	r.False(resp.Connected)
	r.Empty(resp.Destinations)
}

// TestGetDestinations_RejectsWrongType stops a Slack (or webhook-msteams)
// connection being read through the Teams-bot endpoint.
func TestGetDestinations_RejectsWrongType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	org := models.NewOrganization("teams-dest-type", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeMSTeams, "Teams webhook")
	r.NoError(svc.db.CreateChannel(ctx, conn))

	_, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.ErrorIs(err, ErrNotMSTeamsBotChannel)
}

// TestGetDestinations_RejectsCrossOrgAccess is the tenancy negative control.
func TestGetDestinations_RejectsCrossOrgAccess(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-dest-owner", testTenantID)

	intruder := models.NewOrganization("teams-dest-intruder", "")
	r.NoError(svc.db.CreateOrganization(ctx, intruder))

	_, err := svc.GetDestinations(ctx, intruder.Slug, conn.UID)
	r.ErrorIs(err, ErrConnectionNotFound)
}

// TestGetDestinations_UnknownOrg covers the 404 path.
func TestGetDestinations_UnknownOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	_, err := svc.GetDestinations(ctx, "no-such-org", "no-such-uid")
	r.ErrorIs(err, ErrOrganizationNotFound)
}

// TestSetDefaultDestination_RequiresKnownConversation pins that the default
// can only be one of the captured conversation references — a Teams bot
// cannot address a channel it was never added to.
func TestSetDefaultDestination_RequiresKnownConversation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-default-dest", testTenantID)

	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)

	second := installActivity(testTenantID, "19:channel-b", InstallActionAdd)
	second.ChannelData.Channel.Name = "incidents"
	_, err = svc.HandleInstall(ctx, second)
	r.NoError(err)

	dest, err := svc.SetDefaultDestination(ctx, testTenantID, "19:channel-b")
	r.NoError(err)
	r.Equal("incidents", dest.Name)

	conn, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal("19:channel-b", settings.ChannelID)

	_, err = svc.SetDefaultDestination(ctx, testTenantID, "19:never-joined")
	r.ErrorIs(err, ErrDestinationNotFound)
}

// TestCountInstalledTenants_SkipsUninstalled backs the status endpoint.
func TestCountInstalledTenants_SkipsUninstalled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-count-a", testTenantID)
	newConnection(ctx, t, svc, "teams-count-b", "tenant-two")
	// Two orgs on the same tenant must count once.
	newConnection(ctx, t, svc, "teams-count-c", testTenantID)

	count, err := svc.CountInstalledTenants(ctx)
	r.NoError(err)
	r.Equal(2, count)

	r.NoError(svc.HandleUninstall(ctx, "tenant-two"))

	count, err = svc.CountInstalledTenants(ctx)
	r.NoError(err)
	r.Equal(1, count)
}
