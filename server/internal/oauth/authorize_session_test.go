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

	// No org at all: a brand-new account.
	newcomer := models.NewUser("bob@acme.com")
	r.NoError(dbService.CreateUser(ctx, newcomer))

	orgless, err := authSvc.GenerateMCPAccessToken(ctx, newcomer.UID, "", nil, "", time.Hour, "", "")
	r.NoError(err)

	// A session naming an org its holder has left: one still belongs to
	// another org, the other to none.
	other := models.NewOrganization("acme-other", "Acme other")
	r.NoError(dbService.CreateOrganization(ctx, other))

	leaver := models.NewUser("carol@acme.com")
	r.NoError(dbService.CreateUser(ctx, leaver))
	r.NoError(dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(other.UID, leaver.UID, models.MemberRoleUser)))

	staleWithOrg, err := authSvc.GenerateMCPAccessToken(ctx, leaver.UID, "acme", nil, "", time.Hour, "", "")
	r.NoError(err)

	staleNoOrg, err := authSvc.GenerateMCPAccessToken(ctx, newcomer.UID, "acme", nil, "", time.Hour, "", "")
	r.NoError(err)

	return &consentFixture{
		handler: NewHandler(svc, cfg),
		client:  client,
		tokens: map[string]string{
			"orgless":        orgless,
			"stale-with-org": staleWithOrg,
			"stale-no-org":   staleNoOrg,
			"session":        session,
			"mcp-read":       mcpRead,
			"pat":            pat.Token,
			"impersonation":  imper.AccessToken,
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

// A session with no usable org (fclairamb/solidping-business#35) has no org
// to bind a grant to. A user with no org at all is sent to the org-creation
// page carrying the authorize request (which resumes it), never to the
// "/orgs//oauth/consent" dead end; one whose session names an org they left
// but who belongs to another signs in again.
func TestAuthorizeWithoutUsableOrg(t *testing.T) {
	t.Parallel()

	f := setupConsentFixture(t)
	authorizeURI := PathAuthorize + "?" + f.params().Encode()

	for _, tc := range []struct {
		token    string
		wantPath string
	}{
		{"orgless", config.DashboardBasePath + "/no-org"},
		{"stale-no-org", config.DashboardBasePath + "/no-org"},
		{"stale-with-org", config.DashboardBasePath + "/login"},
		// Positive control.
		{"session", config.DashboardBasePath + "/orgs/acme/oauth/consent"},
	} {
		t.Run(tc.token, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, authorizeURI, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+f.tokens[tc.token])

			rec := httptest.NewRecorder()
			r.NoError(f.handler.Authorize(rec, req))
			r.Equal(http.StatusFound, rec.Code, rec.Body.String())

			location := rec.Header().Get("Location")
			r.NotContains(location, "/orgs//")

			dest, err := url.Parse(location)
			r.NoError(err)
			r.Equal(tc.wantPath, dest.Path, location)

			if tc.token != "session" {
				r.Equal(authorizeURI, dest.Query().Get("returnTo"))
			}
		})
	}
}

// Approving without a usable org is refused with a clean 403, never a 500,
// and mints no code.
func TestApproveAuthorizeWithoutUsableOrg(t *testing.T) {
	t.Parallel()

	f := setupConsentFixture(t)

	for _, tok := range []string{"orgless", "stale-no-org", "stale-with-org"} {
		t.Run(tok, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, PathAuthorize,
				strings.NewReader(f.params().Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Authorization", "Bearer "+f.tokens[tok])

			rec := httptest.NewRecorder()
			r.NoError(f.handler.ApproveAuthorize(rec, req))
			r.Equal(http.StatusForbidden, rec.Code, rec.Body.String())
			r.Contains(rec.Body.String(), "FORBIDDEN")
			r.Empty(rec.Header().Get("Location"), "no code may be minted")
		})
	}
}
