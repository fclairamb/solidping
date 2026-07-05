package slack

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestHandleAppUninstalled_FansOutAcrossOrgs covers the spec's uninstall
// acceptance criterion: app_uninstalled must remove the connections of ALL
// orgs connected to that team, since they share the same now-revoked bot
// token. An unrelated team's connection must be untouched.
func TestHandleAppUninstalled_FansOutAcrossOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgA := models.NewOrganization("uninst-fanout-a", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("uninst-fanout-b", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgB))

	orgOther := models.NewOrganization("uninst-fanout-other", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgOther))

	const uninstalledTeamID = "T-UNINSTALLED"
	const otherTeamID = "T-STILL-INSTALLED"

	connA := models.NewIntegration(orgA.UID, models.ConnectionTypeSlack, "Org A workspace")
	connA.Settings["team_id"] = uninstalledTeamID
	r.NoError(svc.db.CreateChannel(ctx, connA))

	connB := models.NewIntegration(orgB.UID, models.ConnectionTypeSlack, "Org B workspace")
	connB.Settings["team_id"] = uninstalledTeamID
	r.NoError(svc.db.CreateChannel(ctx, connB))

	connOther := models.NewIntegration(orgOther.UID, models.ConnectionTypeSlack, "Unrelated workspace")
	connOther.Settings["team_id"] = otherTeamID
	r.NoError(svc.db.CreateChannel(ctx, connOther))

	r.NoError(svc.HandleAppUninstalled(ctx, uninstalledTeamID))

	// Both org A and org B's connections for the uninstalled team are gone.
	remaining, err := svc.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", uninstalledTeamID)
	r.NoError(err)
	r.Empty(remaining, "every org's connection for the uninstalled team must be removed")

	// The unrelated team's connection is untouched.
	otherRemaining, err := svc.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", otherTeamID)
	r.NoError(err)
	r.Len(otherRemaining, 1)
	r.Equal(connOther.UID, otherRemaining[0].UID)
}

// TestHandleAppUninstalled_NoConnectionsIsNoop covers the case where the
// connection(s) were already deleted (e.g. a duplicate/retried webhook) —
// must be a clean no-op, not an error.
func TestHandleAppUninstalled_NoConnectionsIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	r.NoError(svc.HandleAppUninstalled(ctx, "T-NEVER-CONNECTED"))
}

// TestHandleAppUninstalled_SingleOrgStillWorks covers the pre-existing
// single-org-per-workspace case to guard against regressions.
func TestHandleAppUninstalled_SingleOrgStillWorks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("uninst-single-org", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	const teamID = "T-SINGLE-ORG-UNINSTALL"

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Only workspace")
	conn.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, conn))

	r.NoError(svc.HandleAppUninstalled(ctx, teamID))

	remaining, err := svc.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", teamID)
	r.NoError(err)
	r.Empty(remaining)
}
