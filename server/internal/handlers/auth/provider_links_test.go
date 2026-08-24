package auth

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestOAuthErrorRedirectHidesInternals pins spec requirement 3: the HAR from the
// 2026-08-24 incident showed the browser landing on
// `?error=OAUTH_FAILED&error_description=OAuth+failed:+failed+to+find/create+organization:+sql:+no+rows+in+result+set`.
// An unexpected failure must now yield the generic message, with the detail kept
// server-side.
func TestOAuthErrorRedirectHidesInternals(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	handler := NewDiscordOAuthHandler(nil, cfg)

	// The exact production error, wrapper chain included.
	internal := fmt.Errorf("failed to find/create organization: %w", sql.ErrNoRows)

	t.Run("unexpected errors are replaced by the generic description", func(t *testing.T) {
		t.Parallel()

		params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) {
			require.NoError(t, handler.handleOAuthError(w, req, "/dash0/orgs/default", internal))
		})

		require.Equal(t, OAuthCodeFailed, params.Get("error"))
		require.Equal(t, OAuthDescGenericFailure, params.Get("error_description"))
		require.NotContains(t, params.Get("error_description"), "sql: no rows in result set")
		require.NotContains(t, params.Get("error_description"), "find/create organization")
	})

	// Positive control: the typed errors keep their specific, user-actionable
	// text — the fix must not flatten every failure into "try again".
	t.Run("typed errors keep their specific description", func(t *testing.T) {
		t.Parallel()

		params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) {
			require.NoError(t, handler.handleOAuthError(w, req, "/dash0/orgs/default", ErrEmailNotVerified))
		})

		require.Equal(t, OAuthCodeEmailNotVerified, params.Get("error"))
		require.NotEqual(t, OAuthDescGenericFailure, params.Get("error_description"))
		require.Contains(t, params.Get("error_description"), "not verified")
	})

	t.Run("slack takes the same path", func(t *testing.T) {
		t.Parallel()

		slackHandler := NewSlackOAuthHandler(nil, cfg)

		params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) {
			require.NoError(t, slackHandler.handleOAuthError(w, req, "/dash0/orgs/default", internal))
		})

		require.Equal(t, OAuthCodeFailed, params.Get("error"))
		require.Equal(t, OAuthDescGenericFailure, params.Get("error_description"))
	})
}

// redirectParams runs a handler that is expected to issue a redirect and returns
// the query params of its Location header.
func redirectParams(t *testing.T, run func(http.ResponseWriter, *http.Request)) url.Values {
	t.Helper()

	recorder := httptest.NewRecorder()
	run(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord/callback", nil))

	require.Equal(t, http.StatusFound, recorder.Code)

	location, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)

	return location.Query()
}

// TestDeleteOrgReleasesProviderLinks pins the prevention half of the fix: the
// soft-deleted org must not leave a live organization_providers row behind, or
// the very state this spec heals gets re-created on every org deletion.
func TestDeleteOrgReleasesProviderLinks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbService, ctx := setupAuthTestService(t)
	seeded := seedDeletableOrg(ctx, t, dbService, "linked-doomed")

	link := models.NewOrganizationProvider(seeded.org.UID, models.ProviderTypeDiscord, "G-DOOMED")
	r.NoError(dbService.CreateOrganizationProvider(ctx, link))

	_, err := deleteOrgAsOwner(ctx, t, svc, seeded, seeded.org.Slug)
	r.NoError(err)

	_, err = dbService.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, "G-DOOMED")
	r.ErrorIs(err, sql.ErrNoRows, "the deleted org must not keep its guild link")
}

// TestEveryProviderHealsStaleUserLinks is the sweep half of spec requirement 2.
// The bare `return s.db.GetUser(ctx, provider.UserUID)` after a user_providers
// hit was copy-pasted into every connector, so a per-connector proof is the only
// thing that keeps the sweep from silently regressing one file at a time: each
// case seeds a link pointing at a soft-deleted user and asserts the sign-in
// still resolves to a live account.
func TestEveryProviderHealsStaleUserLinks(t *testing.T) {
	t.Parallel()

	dbService := newSQLiteDBService(t)

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
	}
	authSvc := NewService(dbService, cfg.Auth, cfg, nil, nil)

	github := NewGitHubOAuthService(dbService, cfg, authSvc)
	gitlab := NewGitLabOAuthService(dbService, cfg, authSvc)
	google := NewGoogleOAuthService(dbService, cfg, authSvc)
	microsoft := NewMicrosoftOAuthService(dbService, cfg, authSvc)
	oidc := NewOIDCOAuthService(dbService, cfg, authSvc)
	saml := NewSAMLService(dbService, cfg, authSvc)

	cases := []struct {
		name         string
		providerType models.ProviderType
		providerID   string
		signIn       func(ctx context.Context, email string) (*models.User, error)
	}{
		{
			name: "github", providerType: models.ProviderTypeGitHub, providerID: "4242",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return github.findOrCreateUser(ctx, &GitHubUserInfo{ID: 4242, Name: "GH", Email: email})
			},
		},
		{
			name: "gitlab", providerType: models.ProviderTypeGitLab, providerID: "4243",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return gitlab.findOrCreateUser(ctx, &GitLabUserInfo{ID: 4243, Name: "GL", Email: email})
			},
		},
		{
			name: "google", providerType: models.ProviderTypeGoogle, providerID: "google-sub-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return google.findOrCreateUser(ctx, &GoogleUserInfo{
					Sub: "google-sub-1", Email: email, EmailVerified: true, Name: "GO",
				})
			},
		},
		{
			name: "microsoft", providerType: models.ProviderTypeMicrosoft, providerID: "ms-user-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return microsoft.findOrCreateUser(
					ctx, &MicrosoftUserInfo{ID: "ms-user-1", DisplayName: "MS"}, email)
			},
		},
		{
			name: "oidc", providerType: models.ProviderTypeOIDC, providerID: "oidc-sub-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return oidc.findOrCreateUser(ctx, &oidcUserInfo{
					Subject: "oidc-sub-1", Email: email, EmailVerified: true, Name: "OI",
				})
			},
		},
		{
			name: "saml", providerType: models.ProviderTypeSAML, providerID: "saml-nameid-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return saml.findOrCreateUser(ctx, &samlUserInfo{
					NameID: "saml-nameid-1", Email: email, Name: "SA",
				})
			},
		},
		{
			name: "ldap", providerType: models.ProviderTypeLDAP, providerID: "cn=member,dc=acme",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return authSvc.findOrCreateLDAPUser(ctx, &LDAPUserInfo{
					DN: "cn=member,dc=acme", Email: email, Name: "LD",
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			ctx := t.Context()

			dead := models.NewUser("stale-" + tc.name + "@acme.example")
			r.NoError(dbService.CreateUser(ctx, dead))

			staleLink := models.NewUserProvider(dead.UID, tc.providerType, tc.providerID)
			r.NoError(dbService.CreateUserProvider(ctx, staleLink))
			r.NoError(dbService.DeleteUser(ctx, dead.UID))

			user, err := tc.signIn(ctx, "live-"+tc.name+"@acme.example")
			r.NoError(err, "a link pointing at a soft-deleted user must not fail the sign-in")
			r.NotNil(user)
			r.NotEqual(dead.UID, user.UID, "the soft-deleted user must not be resurrected")

			relinked, err := dbService.GetUserProviderByProviderID(ctx, tc.providerType, tc.providerID)
			r.NoError(err)
			r.Equal(user.UID, relinked.UserUID)
			r.NotEqual(staleLink.UID, relinked.UID, "the stale row must have been cleared")
		})
	}
}
