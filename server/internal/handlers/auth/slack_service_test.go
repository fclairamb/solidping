package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func setupSlackTestService(t *testing.T) (*SlackOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := &config.Config{
		Slack: config.SlackConfig{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}

	authService := NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewSlackOAuthService(dbService, cfg, authService)

	return svc, ctx
}

// fakeSlackEndpoints points the Slack sign-in service at httptest stand-ins
// for oauth.v2.access and openid.connect.userInfo, so tests drive the REAL
// HandleCallback (token exchange, user lookup, admission policy) instead of a
// test-local re-implementation of it.
func fakeSlackEndpoints(t *testing.T, svc *SlackOAuthService, teamID, teamName, email string) {
	t.Helper()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"access_token":"xoxb-bot","team":{"id":"` + teamID +
			`","name":"` + teamName + `"},"authed_user":{"id":"U-1","access_token":"xoxp-user"}}`))
	}))
	t.Cleanup(tokenServer.Close)

	userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"sub":"U-1","email":"` + email +
			`","email_verified":true,"name":"Workspace Member",` +
			`"https://slack.com/team_name":"` + teamName + `","https://slack.com/team_domain":"acme"}`))
	}))
	t.Cleanup(userServer.Close)

	svc.oauthURL = tokenServer.URL
	svc.userInfoURL = userServer.URL
}

// TestSlackCallbackAdmitsWorkspaceMember is the end-to-end reproduction of the
// bug this spec fixes, driven through the real HandleCallback: a member of the
// workspace an existing org is linked to signs in and lands *inside* that org.
// The opted-out sub-test is the control proving the admission is the new rule
// (not a blanket "Slack always joins"), and the foreign-workspace sub-test is
// the cross-tenant negative.
func TestSlackCallbackAdmitsWorkspaceMember(t *testing.T) {
	t.Parallel()

	// setup builds an org already linked to workspace T-LINKED and past
	// bootstrap (one seed member), then wires the fake Slack endpoints.
	setup := func(t *testing.T, attestedTeam string) (*SlackOAuthService, *models.Organization, context.Context) {
		t.Helper()

		svc, ctx := setupSlackTestService(t)

		org := models.NewOrganization("linked-org", "Acme")
		require.NoError(t, svc.db.CreateOrganization(ctx, org))
		require.NoError(t, svc.db.CreateOrganizationProvider(
			ctx, models.NewOrganizationProvider(org.UID, models.ProviderTypeSlack, "T-LINKED")))

		seed := models.NewUser("seed@acme.example")
		require.NoError(t, svc.db.CreateUser(ctx, seed))
		require.NoError(t, svc.db.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))

		fakeSlackEndpoints(t, svc, attestedTeam, "Acme", "member@acme.example")

		return svc, org, ctx
	}

	t.Run("workspace member joins the linked org", func(t *testing.T) {
		t.Parallel()

		svc, org, ctx := setup(t, "T-LINKED")

		result, err := svc.HandleCallback(ctx, "mock-code")
		require.NoError(t, err)
		require.False(t, result.Pending, "a member of the linked workspace must be admitted")
		require.Equal(t, org.Slug, result.OrgSlug)
		require.NotEmpty(t, result.RefreshToken, "an admitted user gets a full org-scoped session")

		member, memberErr := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, org.UID)
		require.NoError(t, memberErr)
		require.Equal(t, models.MemberRoleUser, member.Role)
	})

	t.Run("opted-out org still yields a membership request", func(t *testing.T) {
		t.Parallel()

		svc, org, ctx := setup(t, "T-LINKED")
		require.NoError(t, svc.db.SetOrgParameter(
			ctx, org.UID, registrationSlackAutoJoinKey, false, false))

		result, err := svc.HandleCallback(ctx, "mock-code")
		require.NoError(t, err)
		require.True(t, result.Pending)
		require.Empty(t, result.RefreshToken, "no org-scoped session may be minted")

		_, memberErr := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, org.UID)
		require.Error(t, memberErr)

		request, reqErr := svc.db.GetMembershipRequestByOrgAndUser(ctx, org.UID, result.UserUID)
		require.NoError(t, reqErr)
		require.Equal(t, models.MembershipRequestStatusPending, request.Status)
	})

	t.Run("another workspace does not reach the linked org", func(t *testing.T) {
		t.Parallel()

		// Slack attests workspace T-OTHER: the sign-in resolves (creates) that
		// workspace's own org and must never touch the T-LINKED org.
		svc, org, ctx := setup(t, "T-OTHER")

		result, err := svc.HandleCallback(ctx, "mock-code")
		require.NoError(t, err)
		require.NotEqual(t, org.Slug, result.OrgSlug, "a foreign workspace must not resolve to the linked org")

		_, memberErr := svc.db.GetMemberByUserAndOrg(ctx, result.UserUID, org.UID)
		require.Error(t, memberErr, "no membership may appear in the org linked to another workspace")
	})
}

// TestFindOrCreateOrganizationSlugFromWorkspace verifies that the org slug is
// derived from the Slack workspace identity in priority order: team_domain,
// then team_name, then the OAuth Team.Name, then "org".
func TestFindOrCreateOrganizationSlugFromWorkspace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		teamID      string
		orgName     string
		candidates  []string
		wantSlug    string
		wantOrgName string
	}{
		{
			name:        "team_domain wins",
			teamID:      "T1",
			orgName:     "Acme Corp",
			candidates:  []string{"acme", "Acme Corp", ""},
			wantSlug:    "acme",
			wantOrgName: "Acme Corp",
		},
		{
			name:        "empty domain falls back to team_name",
			teamID:      "T2",
			orgName:     "Acme Corp",
			candidates:  []string{"", "Acme Corp", ""},
			wantSlug:    "acme-corp",
			wantOrgName: "Acme Corp",
		},
		{
			name:        "all empty falls back to org",
			teamID:      "T3",
			orgName:     "",
			candidates:  []string{"", "", ""},
			wantSlug:    "org",
			wantOrgName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			svc, ctx := setupSlackTestService(t)

			org, err := svc.findOrCreateOrganization(ctx, tc.teamID, tc.orgName, tc.candidates...)
			r.NoError(err)
			r.NotNil(org)
			r.Equal(tc.wantSlug, org.Slug)
			r.Equal(tc.wantOrgName, org.Name)

			// Calling again with the same team ID returns the same org (idempotent).
			again, err := svc.findOrCreateOrganization(ctx, tc.teamID, tc.orgName, tc.candidates...)
			r.NoError(err)
			r.Equal(org.UID, again.UID)
		})
	}
}

// TestFindOrCreateOrganizationSlugCollision verifies the shared collision loop
// is wired in: a second distinct workspace reusing a taken domain gets a
// numeric suffix rather than reusing the slug.
func TestFindOrCreateOrganizationSlugCollision(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, ctx := setupSlackTestService(t)

	first, err := svc.findOrCreateOrganization(ctx, "TEAM-A", "Acme", "acme")
	r.NoError(err)
	r.Equal("acme", first.Slug)

	second, err := svc.findOrCreateOrganization(ctx, "TEAM-B", "Acme", "acme")
	r.NoError(err)
	r.Equal("acme2", second.Slug)
}
