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

// TestResolveResultChannelUID covers the "land the user where they started"
// requirement (spec proposal #3) directly: a full OAuth exchange can't be
// faked in tests (it calls the real Slack API), so this pins the pure
// decision logic HandleOAuthCallback delegates to instead.
func TestResolveResultChannelUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		targetChannelUID  string
		targetOrgSlug     string
		connUID           string
		wantResultChannel string
	}{
		{
			name:              "channel-edit-page install lands on that channel",
			targetChannelUID:  "channel-uid-1",
			targetOrgSlug:     "acme",
			connUID:           "conn-uid-1",
			wantResultChannel: "channel-uid-1",
		},
		{
			name:              "org-scoped dashboard install with no channel lands on the created connection",
			targetChannelUID:  "",
			targetOrgSlug:     "acme",
			connUID:           "conn-uid-2",
			wantResultChannel: "conn-uid-2",
		},
		{
			name:              "marketplace install (no org in state) lands on org home",
			targetChannelUID:  "",
			targetOrgSlug:     "",
			connUID:           "conn-uid-3",
			wantResultChannel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resolveResultChannelUID(tt.targetChannelUID, tt.targetOrgSlug, tt.connUID)
			require.Equal(t, tt.wantResultChannel, got)
		})
	}
}

// setCommentIngestion rewrites a stored connection's comment_ingestion,
// simulating an operator flipping the dashboard toggle.
func setCommentIngestion(t *testing.T, svc *Service, connUID, mode string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	conn, err := svc.db.GetChannel(ctx, connUID)
	r.NoError(err)

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	r.NoError(err)

	settings.CommentIngestion = mode

	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	r.NoError(svc.db.UpdateChannel(ctx, connUID, &models.IntegrationUpdate{Settings: &settingsMap}))
}

// storedCommentIngestion reads the mode back off the row.
func storedCommentIngestion(t *testing.T, svc *Service, connUID string) string {
	t.Helper()
	r := require.New(t)

	conn, err := svc.db.GetChannel(t.Context(), connUID)
	r.NoError(err)

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	r.NoError(err)

	return settings.CommentIngestion
}

// TestCreateOrUpdateConnection_PreservesCommentIngestion is the re-auth guard:
// the install path rebuilds SlackSettings from the OAuth response, so an
// operator-decided setting must be carried over explicitly or a reconnect
// silently reverts a workspace that opted into capturing every thread reply.
func TestCreateOrUpdateConnection_PreservesCommentIngestion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("reinstall-ingestion", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	oauthResp := fakeOAuthResponse("T-REINSTALL", "Acme")

	connUID, err := svc.createOrUpdateConnection(ctx, org.UID, oauthResp)
	r.NoError(err)

	// A brand-new integration starts explicit.
	r.Equal(models.SlackCommentIngestionExplicit, storedCommentIngestion(t, svc, connUID))

	// The operator deliberately opts into capturing every thread reply…
	setCommentIngestion(t, svc, connUID, models.SlackCommentIngestionAll)

	// …and re-runs the install flow (token refresh, scope change, re-auth).
	sameConnUID, err := svc.createOrUpdateConnection(ctx, org.UID, oauthResp)
	r.NoError(err)
	r.Equal(connUID, sameConnUID, "re-install must update the same row")

	r.Equal(models.SlackCommentIngestionAll, storedCommentIngestion(t, svc, connUID),
		"a re-install must never discard the operator's comment_ingestion choice")

	// The OAuth-owned half is still refreshed, so this is a real re-install and
	// not an accidental no-op that would make the assertion above vacuous.
	conn, err := svc.db.GetChannel(ctx, connUID)
	r.NoError(err)
	r.Equal("xoxb-fake-token", conn.Settings["access_token"])
}

// TestUpdateExistingChannel_PreservesCommentIngestion is the same guard on the
// OTHER re-install path — install triggered from the channel edit page, which
// also rebuilds the settings blob from scratch.
func TestUpdateExistingChannel_PreservesCommentIngestion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("edit-page-ingestion", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Acme")
	conn.Enabled = true
	conn.Settings = models.JSONMap{
		settingsKeyTeamID:   "T-EDITPAGE",
		"access_token":      "xoxb-old",
		"mention_on_call":   true,
		"comment_ingestion": models.SlackCommentIngestionAll,
	}
	r.NoError(svc.db.CreateChannel(ctx, conn))

	updated, err := svc.updateExistingChannel(ctx, conn.UID, fakeOAuthResponse("T-EDITPAGE", "Acme"))
	r.NoError(err)
	r.Equal(conn.UID, updated)

	r.Equal(models.SlackCommentIngestionAll, storedCommentIngestion(t, svc, conn.UID),
		"connecting from the edit page must never discard comment_ingestion")

	// Both operator-owned fields survive, and the credentials really were
	// rewritten (otherwise the assertions above prove nothing).
	after, err := svc.db.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.Equal("xoxb-fake-token", after.Settings["access_token"])
	r.Equal(true, after.Settings["mention_on_call"])
}
