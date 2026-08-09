package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// errSeatCapReached stands in for the entitlements service refusing another
// member (the MaxUsers cap).
var errSeatCapReached = errors.New("membership cap reached")

// cappedEntitlements refuses every membership slot, like an org that has hit
// its MaxUsers entitlement.
type cappedEntitlements struct{}

func (cappedEntitlements) CheckMembership(_ context.Context, _ string) error {
	return errSeatCapReached
}

// setupInstallService wires a Slack integration service with a REAL auth
// service (so the shared admission policy runs for real) and fake Slack OAuth
// endpoints, then returns it alongside its db. entitlements may be nil.
func setupInstallService(
	t *testing.T, teamID string, entitlements auth.EntitlementsChecker,
) (context.Context, *Service) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
		Slack:  config.SlackConfig{ClientID: "test-client-id", ClientSecret: "test-client-secret"},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}

	authService := auth.NewService(dbService, cfg.Auth, cfg, nil, entitlements)
	svc := NewService(dbService, cfg, authService, nil, nil)

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"access_token":"xoxb-bot","bot_user_id":"B1","scope":"chat:write",` +
			`"team":{"id":"` + teamID + `","name":"Acme"},` +
			`"authed_user":{"id":"U-installer","access_token":"xoxp-user"}}`))
	}))
	t.Cleanup(tokenServer.Close)

	userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"sub":"U-installer","email":"installer@acme.example",` +
			`"email_verified":true,"name":"Installer",` +
			`"https://slack.com/team_name":"Acme","https://slack.com/team_domain":"acme"}`))
	}))
	t.Cleanup(userServer.Close)

	svc.oauthURL = tokenServer.URL
	svc.userInfoURL = userServer.URL

	return ctx, svc
}

// runInstallCallback drives the REAL HandleOAuthCallback with a freshly minted
// install state.
func runInstallCallback(ctx context.Context, t *testing.T, svc *Service) *OAuthResult {
	t.Helper()

	authorizeURL, err := svc.BuildInstallURL(ctx, "marketplace", "", "")
	require.NoError(t, err)

	result, err := svc.HandleOAuthCallback(ctx, "mock-code", extractStateParam(t, authorizeURL))
	require.NoError(t, err)

	return result
}

// TestInstallBootstrapsFirstUserAsOwner pins the behavior the old
// ensureOrganizationMembership provided and the shared policy must preserve:
// the first human in a brand-new org created by a Marketplace install gets the
// org's top role. Spec 2026-08-08-11 raised that role from admin to OWNER —
// whoever brings an org into existence owns it — which also means this install
// path can delete the org it just created.
func TestInstallBootstrapsFirstUserAsOwner(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, "T-FRESH", nil)

	result := runInstallCallback(ctx, t, svc)
	r.False(result.Pending)
	r.NotEmpty(result.RefreshToken)

	org, err := svc.db.GetOrganizationBySlug(ctx, result.OrgSlug)
	r.NoError(err)

	member, err := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, org.UID)
	r.NoError(err)
	r.Equal(models.MemberRoleOwner, member.Role)
}

// TestInstallJoinsWorkspaceMemberAsUser is the positive control for the cap
// test below: with a seat available, a workspace member installing into the
// org already linked to that workspace joins as a plain user.
func TestInstallJoinsWorkspaceMemberAsUser(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, linkedTeamID, nil)
	org := seedLinkedOrg(ctx, t, svc)

	result := runInstallCallback(ctx, t, svc)
	r.False(result.Pending)
	r.Equal(org.Slug, result.OrgSlug)

	member, err := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, org.UID)
	r.NoError(err)
	r.Equal(models.MemberRoleUser, member.Role)
}

// TestInstallRespectsMembershipCap is the regression test for the bypass this
// spec closes: the install path used to create a membership unconditionally,
// ignoring the MaxUsers entitlement. Now a capped org leaves the installer
// pending with a membership request — and the bot connection is still stored,
// since the workspace install itself succeeded.
func TestInstallRespectsMembershipCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, linkedTeamID, cappedEntitlements{})
	org := seedLinkedOrg(ctx, t, svc)

	result := runInstallCallback(ctx, t, svc)
	r.True(result.Pending, "a capped org must not admit the installer")
	r.Empty(result.RefreshToken, "no org-scoped session may be minted")
	r.NotEmpty(result.AccessToken, "the installer is authenticated, just not a member")

	_, memberErr := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, org.UID)
	r.Error(memberErr, "no organization_members row may be created past the cap")

	request, reqErr := svc.db.GetMembershipRequestByOrgAndUser(ctx, org.UID, result.UserUID)
	r.NoError(reqErr)
	r.Equal(models.MembershipRequestStatusPending, request.Status)

	// The workspace connection survives: the bot credentials belong to the
	// org, not to the human who clicked install.
	conn, connErr := svc.db.GetChannelByPropertyForOrg(
		ctx, org.UID, string(models.ConnectionTypeSlack), "team_id", linkedTeamID)
	r.NoError(connErr)
	r.NotNil(conn)
}

// TestInstallOptOutLeavesInstallerPending proves the org-level opt-out is
// honored on the install path too.
func TestInstallOptOutLeavesInstallerPending(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, linkedTeamID, nil)
	org := seedLinkedOrg(ctx, t, svc)
	r.NoError(svc.db.SetOrgParameter(ctx, org.UID, "registration.slack_workspace_auto_join", false, false))

	result := runInstallCallback(ctx, t, svc)
	r.True(result.Pending)

	_, memberErr := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, org.UID)
	r.Error(memberErr)
}

// TestInstallCallbackRedirectsPendingToNoOrg covers the handler tail: a
// pending install must land on the shared request-access surface instead of
// the post-install exchange page (which needs a refresh token an org-less
// session does not have).
func TestInstallCallbackRedirectsPendingToNoOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, linkedTeamID, cappedEntitlements{})
	org := seedLinkedOrg(ctx, t, svc)

	authorizeURL, err := svc.BuildInstallURL(ctx, "marketplace", "", "")
	r.NoError(err)

	handler := NewHandler(svc, svc.cfg)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/api/v1/integrations/slack/oauth?code=mock-code&state="+
			extractStateParam(t, authorizeURL), nil)
	rec := httptest.NewRecorder()

	r.NoError(handler.OAuthCallback(rec, req))
	r.Equal(http.StatusFound, rec.Code)

	location := rec.Header().Get("Location")
	r.Contains(location, "/dash0/no-org")
	r.Contains(location, "membershipPending="+org.Slug)
	r.NotContains(location, "/dash0/auth/slack/complete")
}

// TestInstallDoesNotJoinForeignTargetOrg is the cross-tenant negative for the
// install path: an install that targets an org by slug (the channel-edit-page
// variant) must not grant membership to someone who is neither a member of
// that org nor a member of the workspace it is linked to. Before this spec the
// install path created the membership unconditionally.
func TestInstallDoesNotJoinForeignTargetOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupInstallService(t, linkedTeamID, nil)

	// The org the install URL points at belongs to a DIFFERENT workspace.
	foreign := models.NewOrganization("foreign-org", "Foreign")
	r.NoError(svc.db.CreateOrganization(ctx, foreign))
	r.NoError(svc.db.CreateOrganizationProvider(
		ctx, models.NewOrganizationProvider(foreign.UID, models.ProviderTypeSlack, "T-FOREIGN")))

	seed := models.NewUser("seed@foreign.example")
	r.NoError(svc.db.CreateUser(ctx, seed))
	r.NoError(svc.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(foreign.UID, seed.UID, models.MemberRoleAdmin)))

	authorizeURL, err := svc.BuildInstallURL(ctx, "dashboard", "", foreign.Slug)
	r.NoError(err)

	result, err := svc.HandleOAuthCallback(ctx, "mock-code", extractStateParam(t, authorizeURL))
	r.NoError(err)
	r.True(result.Pending, "workspace T-LINKED must not open the org linked to T-FOREIGN")
	r.Equal(foreign.Slug, result.OrgSlug)

	_, memberErr := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, foreign.UID)
	r.Error(memberErr, "no organization_members row may be created in a foreign org")
}

// linkedTeamID is the workspace the seeded org is linked to.
const linkedTeamID = "T-LINKED"

// seedLinkedOrg creates an org linked to linkedTeamID and already past the
// zero-member bootstrap rule.
func seedLinkedOrg(ctx context.Context, t *testing.T, svc *Service) *models.Organization {
	t.Helper()

	org := models.NewOrganization("linked-org", "Acme")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))
	require.NoError(t, svc.db.CreateOrganizationProvider(
		ctx, models.NewOrganizationProvider(org.UID, models.ProviderTypeSlack, linkedTeamID)))

	seed := models.NewUser("seed@acme.example")
	require.NoError(t, svc.db.CreateUser(ctx, seed))
	require.NoError(t, svc.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))

	return org
}
