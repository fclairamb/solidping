package slack

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestCountInstalledTeams_CountsDistinctTeamsNotRows covers the spec's
// Socket Mode status acceptance criterion: a workspace connected to two
// orgs must count as ONE installed team, not two connection rows.
func TestCountInstalledTeams_CountsDistinctTeamsNotRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgA := models.NewOrganization("count-teams-org-a", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("count-teams-org-b", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgB))

	orgC := models.NewOrganization("count-teams-org-c", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgC))

	const sharedTeamID = "T-COUNT-SHARED"
	const distinctTeamID = "T-COUNT-DISTINCT"

	// Same workspace, two orgs — must count once.
	connA := models.NewIntegration(orgA.UID, models.ConnectionTypeSlack, "Shared workspace (org A)")
	connA.Settings["team_id"] = sharedTeamID
	r.NoError(svc.db.CreateChannel(ctx, connA))

	connB := models.NewIntegration(orgB.UID, models.ConnectionTypeSlack, "Shared workspace (org B)")
	connB.Settings["team_id"] = sharedTeamID
	r.NoError(svc.db.CreateChannel(ctx, connB))

	// A distinct workspace in a third org — must count separately.
	connC := models.NewIntegration(orgC.UID, models.ConnectionTypeSlack, "Distinct workspace (org C)")
	connC.Settings["team_id"] = distinctTeamID
	r.NoError(svc.db.CreateChannel(ctx, connC))

	count, err := svc.CountInstalledTeams(ctx)
	r.NoError(err)
	r.Equal(2, count, "3 connection rows across 2 distinct team_ids must count as 2, not 3")
}

// TestCountInstalledTeams_TwoOrgWorkspaceCountsAsOne pins the acceptance
// criterion's exact wording: "Socket Mode status reports 1 installed team
// for a two-org workspace" — a single Slack workspace connected to two orgs
// and nothing else must report exactly 1, not 2.
func TestCountInstalledTeams_TwoOrgWorkspaceCountsAsOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgA := models.NewOrganization("count-two-org-a", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("count-two-org-b", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgB))

	const teamID = "T-TWO-ORG-WORKSPACE"

	connA := models.NewIntegration(orgA.UID, models.ConnectionTypeSlack, "Two-org workspace (A)")
	connA.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connA))

	connB := models.NewIntegration(orgB.UID, models.ConnectionTypeSlack, "Two-org workspace (B)")
	connB.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connB))

	count, err := svc.CountInstalledTeams(ctx)
	r.NoError(err)
	r.Equal(1, count)
}

// TestCountInstalledTeams_ExcludesNonSlackAndDeleted covers that the count
// is scoped to type=slack and live rows only.
func TestCountInstalledTeams_ExcludesNonSlackAndDeleted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("count-teams-excl", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	// A live Slack connection — counted.
	liveConn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Live workspace")
	liveConn.Settings["team_id"] = "T-COUNT-LIVE"
	r.NoError(svc.db.CreateChannel(ctx, liveConn))

	// A soft-deleted Slack connection — not counted.
	deletedConn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Deleted workspace")
	deletedConn.Settings["team_id"] = "T-COUNT-DELETED"
	r.NoError(svc.db.CreateChannel(ctx, deletedConn))
	r.NoError(svc.db.DeleteChannel(ctx, deletedConn.UID))

	// A non-Slack connection — not counted even though it's a live row.
	discordConn := models.NewIntegration(org.UID, models.ConnectionTypeDiscord, "Discord channel")
	r.NoError(svc.db.CreateChannel(ctx, discordConn))

	count, err := svc.CountInstalledTeams(ctx)
	r.NoError(err)
	r.Equal(1, count)
}

// TestCountInstalledTeams_NoConnectionsIsZero covers the empty-state case.
func TestCountInstalledTeams_NoConnectionsIsZero(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	count, err := svc.CountInstalledTeams(ctx)
	r.NoError(err)
	r.Equal(0, count)
}
