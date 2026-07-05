package slack

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeOAuthResponse builds a minimal *OAuthResponse for exercising
// createOrUpdateConnection without a real Slack token exchange.
func fakeOAuthResponse(teamID, teamName string) *OAuthResponse {
	resp := &OAuthResponse{
		OK:          true,
		AccessToken: "xoxb-fake-token",
		BotUserID:   "UBOTFAKE",
		Scope:       "chat:write,channels:read",
	}
	resp.Team.ID = teamID
	resp.Team.Name = teamName
	resp.AuthedUser.ID = "UFAKEUSER"

	return resp
}

// TestCreateOrUpdateConnection_TwoOrgsOneWorkspace covers the spec's
// two-orgs-one-workspace acceptance criterion: installing the same Slack
// workspace (team_id) into a second org must create a NEW connection row
// scoped to that org, and must leave the first org's row untouched.
func TestCreateOrUpdateConnection_TwoOrgsOneWorkspace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgA := models.NewOrganization("two-orgs-a", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("two-orgs-b", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgB))

	const teamID = "T-SHARED"

	oauthResp := fakeOAuthResponse(teamID, "Shared Workspace")

	connUIDInA, err := svc.createOrUpdateConnection(ctx, orgA.UID, oauthResp)
	r.NoError(err)
	r.NotEmpty(connUIDInA)

	connUIDInB, err := svc.createOrUpdateConnection(ctx, orgB.UID, oauthResp)
	r.NoError(err)
	r.NotEmpty(connUIDInB)

	r.NotEqual(connUIDInA, connUIDInB, "each org must get its own connection row")

	// Org A's connection is untouched: still resolvable, still owned by org A.
	connA, err := svc.db.GetChannel(ctx, connUIDInA)
	r.NoError(err)
	r.Equal(orgA.UID, connA.OrganizationUID)

	connB, err := svc.db.GetChannel(ctx, connUIDInB)
	r.NoError(err)
	r.Equal(orgB.UID, connB.OrganizationUID)

	// Both rows carry the same team_id (they are the same physical
	// workspace/bot), scoped to different orgs.
	r.Equal(teamID, connA.Settings["team_id"])
	r.Equal(teamID, connB.Settings["team_id"])
}

// TestCreateOrUpdateConnection_SameOrgReinstall covers the spec's
// same-org-reinstall acceptance criterion: re-running the install flow for
// an org that already has a connection for this team_id must update that
// row (idempotent, token refresh) rather than creating a duplicate.
func TestCreateOrUpdateConnection_SameOrgReinstall(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("reinstall-org", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	const teamID = "T-REINSTALL"

	firstResp := fakeOAuthResponse(teamID, "Reinstall Workspace")

	firstUID, err := svc.createOrUpdateConnection(ctx, org.UID, firstResp)
	r.NoError(err)
	r.NotEmpty(firstUID)

	// Re-install: same org, same team, fresh token.
	secondResp := fakeOAuthResponse(teamID, "Reinstall Workspace")
	secondResp.AccessToken = "xoxb-refreshed-token"

	secondUID, err := svc.createOrUpdateConnection(ctx, org.UID, secondResp)
	r.NoError(err)

	r.Equal(firstUID, secondUID, "re-install in the same org must update the existing row, not create a new one")

	// Only one connection row exists for this org/team.
	conn, err := svc.db.GetChannelByPropertyForOrg(
		ctx, org.UID, string(models.ConnectionTypeSlack), "team_id", teamID)
	r.NoError(err)
	r.Equal(firstUID, conn.UID)

	// Settings were refreshed with the new token.
	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal("xoxb-refreshed-token", settings.AccessToken)
}
