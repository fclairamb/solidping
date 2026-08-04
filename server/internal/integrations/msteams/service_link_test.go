package msteams

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// startLinkFor mints a link code for a fresh org and returns (orgUID, code).
func startLinkFor(ctx context.Context, t *testing.T, svc *Service, slug string) (string, string) {
	t.Helper()

	org := models.NewOrganization(slug, "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	pending, err := svc.StartLink(ctx, org.UID, "")
	require.NoError(t, err)
	require.NotEmpty(t, pending.Code)

	return org.UID, pending.Code
}

// TestLinkTenant_BindsTenantFromVerifiedActivity is the positive control for
// the proof-of-possession model: the tenant id is written only after a code
// minted for THIS org is quoted back from inside a real activity.
func TestLinkTenant_BindsTenantFromVerifiedActivity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	orgUID, code := startLinkFor(ctx, t, svc, "teams-link-ok")

	conn, err := svc.LinkTenant(ctx, code, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)
	r.Equal(orgUID, conn.OrganizationUID)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal(testTenantID, settings.TenantID)
	// Linking also registers the conversation it was sent from.
	r.Len(settings.Destinations, 1)
	r.Equal("19:channel-a", settings.ChannelID)
}

// TestLinkTenant_CodeIsSingleUse stops a leaked code being replayed to attach
// a second tenant (or re-attach after an admin revokes access).
func TestLinkTenant_CodeIsSingleUse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	_, code := startLinkFor(ctx, t, svc, "teams-link-once")

	_, err := svc.LinkTenant(ctx, code, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)

	_, err = svc.LinkTenant(ctx, code, messageActivity("second-tenant", "19:channel-a", ""))
	r.ErrorIs(err, ErrInvalidLinkCode)
}

// TestLinkTenant_RejectsUnknownCode is the core negative control. Without it
// every other assertion here would pass against an implementation that
// bound whatever tenant asked.
func TestLinkTenant_RejectsUnknownCode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	_, err := svc.LinkTenant(ctx, "ABCDE-FGHIJ", messageActivity(testTenantID, "19:channel-a", ""))
	r.ErrorIs(err, ErrInvalidLinkCode)
}

// TestLinkTenant_CannotStealAnotherOrgsTenant is the regression test for the
// cross-tenant hijack. Org A legitimately holds the tenant; org B mints its
// own code and redeems it from inside the SAME tenant. That must fail — and
// crucially, A's connection must be untouched, since the whole point of the
// original bug was that a second claimant silently absorbed the first one's
// conversation references.
func TestLinkTenant_CannotStealAnotherOrgsTenant(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	_, codeA := startLinkFor(ctx, t, svc, "teams-steal-a")

	victim, err := svc.LinkTenant(ctx, codeA, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)

	_, codeB := startLinkFor(ctx, t, svc, "teams-steal-b")

	_, err = svc.LinkTenant(ctx, codeB, messageActivity(testTenantID, "19:channel-a", ""))
	r.ErrorIs(err, ErrTenantAlreadyLinked)

	// The victim's connection still owns the tenant, and routing still
	// resolves to it.
	resolved, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)
	r.Equal(victim.UID, resolved.UID)
}

// TestStartLink_DoesNotBindATenant pins that the dashboard half of the flow
// creates a connection with NO tenant. A connection that could be created
// already claiming a tenant is exactly the hole this design closes.
func TestStartLink_DoesNotBindATenant(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	org := models.NewOrganization("teams-link-empty", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	pending, err := svc.StartLink(ctx, org.UID, "")
	r.NoError(err)

	conn, err := svc.db.GetChannel(ctx, pending.ConnectionUID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Empty(settings.TenantID)

	// And an unlinked connection is not routable.
	_, err = svc.GetConnectionByTenantID(ctx, testTenantID)
	r.ErrorIs(err, ErrTenantNotLinked)
}

// TestStartLink_RefusesAlreadyLinkedConnection stops a second code silently
// moving a live tenant's notifications elsewhere.
func TestStartLink_RefusesAlreadyLinkedConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	orgUID, code := startLinkFor(ctx, t, svc, "teams-link-relink")

	conn, err := svc.LinkTenant(ctx, code, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)

	_, err = svc.StartLink(ctx, orgUID, conn.UID)
	r.ErrorIs(err, ErrConnectionAlreadyLinked)
}

// TestStartLink_RejectsAnotherOrgsConnection is the tenancy check on the
// dashboard side.
func TestStartLink_RejectsAnotherOrgsConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-link-owner", testTenantID)

	intruder := models.NewOrganization("teams-link-other", "")
	r.NoError(svc.db.CreateOrganization(ctx, intruder))

	_, err := svc.StartLink(ctx, intruder.UID, conn.UID)
	r.ErrorIs(err, ErrConnectionNotFound)
}

// TestLinkCodeNormalization keeps redemption forgiving of how a human retypes
// a code while still requiring the right characters.
func TestLinkCodeNormalization(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	_, code := startLinkFor(ctx, t, svc, "teams-link-norm")

	// Lowercased, spaced, separator dropped — same code.
	mangled := " " + strings.ToLower(strings.ReplaceAll(code, "-", " ")) + " "

	_, err := svc.LinkTenant(ctx, mangled, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)
}

// TestDispatchActivity_LinkCommandEndToEnd drives the whole flow through the
// chat command, which is how a real admin performs it.
func TestDispatchActivity_LinkCommandEndToEnd(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	_, code := startLinkFor(ctx, t, svc, "teams-link-chat")

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> link "+code)))

	calls := connector.recorded()
	r.NotEmpty(calls)
	r.Contains(cardJSON(t, calls[len(calls)-1]), "SolidPing is connected")

	conn, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)
	r.NotNil(conn)
}

// TestDispatchActivity_LinkCommandRejectsBadCode checks the failure reply is
// generic — a distinguishable error would turn the command into an oracle for
// probing other organizations' codes.
func TestDispatchActivity_LinkCommandRejectsBadCode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	r.NoError(DispatchActivity(ctx, svc,
		messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> link ZZZZZ-ZZZZZ")))

	calls := connector.recorded()
	r.NotEmpty(calls)
	r.Contains(cardJSON(t, calls[len(calls)-1]), "invalid or has expired")

	_, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.ErrorIs(err, ErrTenantNotLinked)
}

// TestLinkTenant_HonorsSingleTenantPin covers the self-hosted pin on the
// linking path.
func TestLinkTenant_HonorsSingleTenantPin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	svc.cfg.MSTeams.TenantID = "the-only-allowed-tenant"

	_, code := startLinkFor(ctx, t, svc, "teams-link-pin")

	_, err := svc.LinkTenant(ctx, code, messageActivity("some-other-tenant", "19:channel-a", ""))
	r.ErrorIs(err, ErrTenantNotAllowed)
}

// TestLinkTenant_RefusesFromPrivateChat pins the mitigation for the fact that
// Bot Framework cannot tell us whether the sender is a team owner without
// Microsoft Graph consent this integration does not request: the link must at
// least happen somewhere the tenant's other members can see it.
func TestLinkTenant_RefusesFromPrivateChat(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	_, code := startLinkFor(ctx, t, svc, "teams-link-dm")

	activity := messageActivity(testTenantID, "19:dm", "")
	activity.Conversation.ConversationType = "personal"

	_, err := svc.LinkTenant(ctx, code, activity)
	r.ErrorIs(err, ErrLinkRequiresChannel)

	// And nothing was bound.
	_, err = svc.GetConnectionByTenantID(ctx, testTenantID)
	r.ErrorIs(err, ErrTenantNotLinked)
}

// TestLinkTenant_UninstallReleasesTheTenantForRelinking is the recovery path
// for a tenant that was linked to the wrong organization.
//
// Only the holding org can delete its own integration, so without this a bad
// link would be permanent from the tenant's point of view. Removing the app in
// Teams is the one lever the tenant's own admin controls, and it now releases
// the claim: reinstall, mint a code in the right org, link again.
func TestLinkTenant_UninstallReleasesTheTenantForRelinking(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	// The wrong org links first.
	wrongOrgUID, wrongCode := startLinkFor(ctx, t, svc, "teams-recover-bad")

	wrongConn, err := svc.LinkTenant(ctx, wrongCode, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)
	r.Equal(wrongOrgUID, wrongConn.OrganizationUID)

	// The right org cannot displace a LIVE claim.
	rightOrgUID, blockedCode := startLinkFor(ctx, t, svc, "teams-recover-ok")

	_, err = svc.LinkTenant(ctx, blockedCode, messageActivity(testTenantID, "19:channel-a", ""))
	r.ErrorIs(err, ErrTenantAlreadyLinked)

	// The tenant's own admin removes the app in Teams.
	r.NoError(svc.HandleUninstall(ctx, testTenantID))

	// Now a fresh code from the right org succeeds.
	pending, err := svc.StartLink(ctx, rightOrgUID, "")
	r.NoError(err)

	conn, err := svc.LinkTenant(ctx, pending.Code, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)
	r.Equal(rightOrgUID, conn.OrganizationUID)

	// Routing now resolves to the right org, and the stale row released its
	// claim rather than leaving an ambiguous duplicate.
	resolved, err := svc.GetConnectionByTenantID(ctx, testTenantID)
	r.NoError(err)
	r.Equal(conn.UID, resolved.UID)

	stale, err := svc.db.GetChannel(ctx, wrongConn.UID)
	r.NoError(err)

	staleSettings, err := models.MSTeamsBotSettingsFromJSONMap(stale.Settings)
	r.NoError(err)
	r.Empty(staleSettings.TenantID)
}

// TestLinkTenant_RecordsWhoLinked keeps an audit trail of the user that bound
// the tenant.
func TestLinkTenant_RecordsWhoLinked(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	_, code := startLinkFor(ctx, t, svc, "teams-link-audit")

	conn, err := svc.LinkTenant(ctx, code, messageActivity(testTenantID, "19:channel-a", ""))
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal("29:user", settings.InstalledByUserID)
}
