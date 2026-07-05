package slack

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestGetConnectionByTeamID_HomeOrgResolution covers the spec's primary
// acceptance criterion: a workspace connected to orgs A (home) and B always
// resolves to A, regardless of which connection was created first.
func TestGetConnectionByTeamID_HomeOrgResolution(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgHome := models.NewOrganization("gcbti-hor-home", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgHome))

	orgOther := models.NewOrganization("gcbti-hor-other", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgOther))

	const teamID = "T-HOME-ORG-RESOLUTION"

	// The "other" org's connection is created FIRST — if oldest-first
	// fallback logic were used instead of the home-org lookup, it would win.
	// The test asserts the home org wins regardless.
	connOther := models.NewIntegration(orgOther.UID, models.ConnectionTypeSlack, "Other org workspace")
	connOther.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connOther))

	connHome := models.NewIntegration(orgHome.UID, models.ConnectionTypeSlack, "Home org workspace")
	connHome.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connHome))

	// Record orgHome as the home org (organization_providers row) — this is
	// what "first install wins" / Sign-in-with-Slack / Marketplace installs
	// already populate.
	provider := models.NewOrganizationProvider(orgHome.UID, models.ProviderTypeSlack, teamID)
	r.NoError(svc.db.CreateOrganizationProvider(ctx, provider))

	got, err := svc.GetConnectionByTeamID(ctx, teamID)
	r.NoError(err)
	r.Equal(connHome.UID, got.UID, "must resolve to the home org's connection, not the older one")
	r.Equal(orgHome.UID, got.OrganizationUID)
}

// TestGetConnectionByTeamID_HomeOrgConnectionDeletedFallsBack covers the case
// where organization_providers still points at the home org, but that org's
// own connection was deleted (e.g. manually disconnected) while other orgs'
// connections for the same team remain live. Routing must not hard-fail —
// it falls back to the deterministic (oldest remaining) resolution.
func TestGetConnectionByTeamID_HomeOrgConnectionDeletedFallsBack(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgHome := models.NewOrganization("gcbti-hcd-home", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgHome))

	orgOther := models.NewOrganization("gcbti-hcd-other", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgOther))

	const teamID = "T-HOME-CONN-DELETED"

	connHome := models.NewIntegration(orgHome.UID, models.ConnectionTypeSlack, "Home org workspace")
	connHome.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connHome))
	r.NoError(svc.db.DeleteChannel(ctx, connHome.UID))

	connOther := models.NewIntegration(orgOther.UID, models.ConnectionTypeSlack, "Other org workspace")
	connOther.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connOther))

	provider := models.NewOrganizationProvider(orgHome.UID, models.ProviderTypeSlack, teamID)
	r.NoError(svc.db.CreateOrganizationProvider(ctx, provider))

	got, err := svc.GetConnectionByTeamID(ctx, teamID)
	r.NoError(err)
	r.Equal(connOther.UID, got.UID, "must fall back to the remaining connection when the home org's own row is gone")
}

// TestGetConnectionByTeamID_NoProviderRowFallsBackToOldest covers a workspace
// that was only ever dashboard-installed (no organization_providers row):
// resolution must be deterministic — the oldest connection wins.
func TestGetConnectionByTeamID_NoProviderRowFallsBackToOldest(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgFirst := models.NewOrganization("gcbti-npr-first", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgFirst))

	orgSecond := models.NewOrganization("gcbti-npr-second", "")
	r.NoError(svc.db.CreateOrganization(ctx, orgSecond))

	const teamID = "T-NO-PROVIDER-ROW"

	connFirst := models.NewIntegration(orgFirst.UID, models.ConnectionTypeSlack, "First workspace connection")
	connFirst.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connFirst))

	connSecond := models.NewIntegration(orgSecond.UID, models.ConnectionTypeSlack, "Second workspace connection")
	connSecond.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, connSecond))

	// No organization_providers row is created for this team.
	got, err := svc.GetConnectionByTeamID(ctx, teamID)
	r.NoError(err)
	r.Equal(connFirst.UID, got.UID, "must resolve to the oldest connection when there is no home org")
}

// TestGetConnectionByTeamID_SingleConnectionNoProviderRow covers the common
// single-org case (no home org recorded, exactly one connection) — must
// resolve cleanly without hitting the ambiguity warning path.
func TestGetConnectionByTeamID_SingleConnectionNoProviderRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("gcbti-single-conn", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	const teamID = "T-SINGLE-CONNECTION"

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Only workspace connection")
	conn.Settings["team_id"] = teamID
	r.NoError(svc.db.CreateChannel(ctx, conn))

	got, err := svc.GetConnectionByTeamID(ctx, teamID)
	r.NoError(err)
	r.Equal(conn.UID, got.UID)
}

// TestGetConnectionByTeamID_NothingFoundReturnsNotFound covers the
// not-connected-at-all case: no provider row, no connections.
func TestGetConnectionByTeamID_NothingFoundReturnsNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	_, err := svc.GetConnectionByTeamID(ctx, "T-DOES-NOT-EXIST")
	r.ErrorIs(err, ErrConnectionNotFound)
}
