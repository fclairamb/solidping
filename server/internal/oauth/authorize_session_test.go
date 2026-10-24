package oauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// Consent accepts dashboard sessions only (spec 2026-10-16-12): a PAT, an MCP
// access token or an impersonation token must never approve a grant.

type consentFixture struct {
	handler *Handler
	client  *models.OAuthClient
	tokens  map[string]string
}

func setupConsentFixture(t *testing.T) *consentFixture {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: oauthTestIssuer},
		Auth: config.AuthConfig{
			JWTSecret:            "test-jwt-secret",
			AccessTokenExpiry:    time.Hour,
			RefreshTokenExpiry:   7 * 24 * time.Hour,
			ImpersonationEnabled: true,
		},
	}

	authSvc := auth.NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewService(dbService, authSvc, cfg)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	admin := models.NewUser("root@acme.com")
	admin.SuperAdmin = true
	r.NoError(dbService.CreateUser(ctx, admin))

	user := models.NewUser("alice@acme.com")
	r.NoError(dbService.CreateUser(ctx, user))
	r.NoError(dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	client, _, err := svc.RegisterClient(ctx, "Third party", []string{testRedirectURI},
		[]string{GrantAuthorizationCode, GrantRefreshToken}, []string{ScopeMCP, ScopeMCPRead}, true)
	r.NoError(err)

	session, err := authSvc.GenerateMCPAccessToken(ctx, user.UID, "acme", nil, "", time.Hour, "", "")
	r.NoError(err)

	mcpRead, err := authSvc.GenerateMCPAccessToken(ctx, user.UID, "acme", []string{ScopeMCPRead},
		oauthTestIssuer+"/api/v1/mcp", time.Hour, "", client.ClientID)
	r.NoError(err)

	pat, err := authSvc.CreatePAT(ctx, "acme", user.UID, auth.CreateTokenRequest{Name: "pat"})
	r.NoError(err)

	imper, err := authSvc.Impersonate(ctx, admin.UID, user.UID, "acme", auth.Context{})
	r.NoError(err)

	return &consentFixture{
		handler: NewHandler(svc, cfg),
		client:  client,
		tokens: map[string]string{
			"session":       session,
			"mcp-read":      mcpRead,
			"pat":           pat.Token,
			"impersonation": imper.AccessToken,
		},
	}
}

func (f *consentFixture) params() url.Values {
	return url.Values{
		paramClientID:           {f.client.ClientID},
		"redirect_uri":          {testRedirectURI},
		"response_type":         {ResponseTypeCode},
		"scope":                 {ScopeMCP},
		"state":                 {"xyz"},
		"code_challenge":        {challengeFor(testVerifier)},
		"code_challenge_method": {CodeChallengeMethodS256},
	}
}

func TestApproveAuthorizeAcceptsDashboardSessionsOnly(t *testing.T) {
	t.Parallel()

	f := setupConsentFixture(t)

	for _, tc := range []struct {
		token    string
		wantCode int
	}{
		{"pat", http.StatusUnauthorized},
		{"mcp-read", http.StatusUnauthorized},
		{"impersonation", http.StatusUnauthorized},
		// Positive control.
		{"session", http.StatusFound},
	} {
		t.Run(tc.token, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, PathAuthorize,
				strings.NewReader(f.params().Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Authorization", "Bearer "+f.tokens[tc.token])

			rec := httptest.NewRecorder()
			require.NoError(t, f.handler.ApproveAuthorize(rec, req))
			require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())

			location := rec.Header().Get("Location")
			if tc.wantCode != http.StatusFound {
				require.Empty(t, location, "no code may be minted")

				return
			}

			dest, err := url.Parse(location)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(location, testRedirectURI), location)
			require.NotEmpty(t, dest.Query().Get("code"))
		})
	}
}

func TestAuthorizeBouncesNonSessionCredentialsToLogin(t *testing.T) {
	t.Parallel()

	f := setupConsentFixture(t)

	for _, tok := range []string{"pat", "mcp-read"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			PathAuthorize+"?"+f.params().Encode(), http.NoBody)
		req.Header.Set("Authorization", "Bearer "+f.tokens[tok])

		rec := httptest.NewRecorder()
		require.NoError(t, f.handler.Authorize(rec, req))
		require.Equal(t, http.StatusFound, rec.Code, tok)
		require.True(t, strings.HasPrefix(rec.Header().Get("Location"), config.DashboardBasePath+"/login"),
			"%s: %s", tok, rec.Header().Get("Location"))
	}

	// Positive control: a dashboard session goes to the consent screen.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		PathAuthorize+"?"+f.params().Encode(), http.NoBody)
	req.Header.Set("Authorization", "Bearer "+f.tokens["session"])

	rec := httptest.NewRecorder()
	require.NoError(t, f.handler.Authorize(rec, req))
	require.Equal(t, http.StatusFound, rec.Code)
	require.NotContains(t, rec.Header().Get("Location"), "/login")
}
