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

		params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) error {
			return handler.handleOAuthError(w, req, "/dash0/orgs/default", internal)
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

		params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) error {
			return handler.handleOAuthError(w, req, "/dash0/orgs/default", ErrEmailNotVerified)
		})

		require.Equal(t, OAuthCodeEmailNotVerified, params.Get("error"))
		require.NotEqual(t, OAuthDescGenericFailure, params.Get("error_description"))
		require.Contains(t, params.Get("error_description"), "not verified")
	})

	t.Run("slack takes the same path", func(t *testing.T) {
		t.Parallel()

		slackHandler := NewSlackOAuthHandler(nil, cfg)

		params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) error {
			return slackHandler.handleOAuthError(w, req, "/dash0/orgs/default", internal)
		})

		require.Equal(t, OAuthCodeFailed, params.Get("error"))
		require.Equal(t, OAuthDescGenericFailure, params.Get("error_description"))
	})
}

// redirectParams runs a handler that is expected to issue a redirect and returns
// the query params of its Location header.
func redirectParams(t *testing.T, run func(http.ResponseWriter, *http.Request) error) url.Values {
	t.Helper()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/auth/discord/callback", nil)
	require.NoError(t, run(recorder, req))

	require.Equal(t, http.StatusFound, recorder.Code)

	location, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)

	return location.Query()
}

// TestDeleteOrgReleasesProviderLinks pins the prevention half of the fix: the
// soft-deleted org must not leave a live organization_providers row behind, or
// the very state this spec heals gets re-created on every org deletion.
//
// It is deliberately re-asserted per provider type and for an org holding
// SEVERAL links: releaseOrgProviderLinks loops, and a regression that released
// only the first row would still pass a single-link test while re-creating the
// exact field state spec 2026-08-27-02 had to heal by hand.
func TestDeleteOrgReleasesProviderLinks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbService, ctx := setupAuthTestService(t)
	seeded := seedDeletableOrg(ctx, t, dbService, "linked-doomed")

	guildLink := models.NewOrganizationProvider(seeded.org.UID, models.ProviderTypeDiscord, "G-DOOMED")
	r.NoError(dbService.CreateOrganizationProvider(ctx, guildLink))

	workspaceLink := models.NewOrganizationProvider(seeded.org.UID, models.ProviderTypeSlack, "T0ACME0003")
	r.NoError(dbService.CreateOrganizationProvider(ctx, workspaceLink))

	// Positive control: a link belonging to ANOTHER org must survive the
	// deletion — the release is scoped to the org being deleted, not a purge.
	survivor := seedDeletableOrg(ctx, t, dbService, "linked-keeper")
	survivorLink := models.NewOrganizationProvider(survivor.org.UID, models.ProviderTypeSlack, "T0ACME0001")
	r.NoError(dbService.CreateOrganizationProvider(ctx, survivorLink))

	_, err := deleteOrgAsOwner(ctx, t, svc, seeded, seeded.org.Slug)
	r.NoError(err)

	_, err = dbService.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, "G-DOOMED")
	r.ErrorIs(err, sql.ErrNoRows, "the deleted org must not keep its guild link")

	_, err = dbService.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, "T0ACME0003")
	r.ErrorIs(err, sql.ErrNoRows, "the deleted org must not keep its workspace link either")

	kept, err := dbService.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, "T0ACME0001")
	r.NoError(err, "another org's link must be untouched")
	r.Equal(survivorLink.UID, kept.UID)

	// And with the links released, nothing dangles: the boot-time counter that
	// makes this failure mode visible reports zero.
	dangling, err := dbService.CountDanglingOrganizationProviders(ctx)
	r.NoError(err)
	r.Zero(dangling, "a released link is not a dangling link")
}

// TestCountDanglingOrganizationProviders covers the observability half of spec
// 2026-08-27-02 (its point 6). A dangling link is otherwise completely silent —
// the healers only fire when somebody trips over one — so this count is the
// only thing that tells an operator a workspace is bricked before they hear it
// from the customer.
func TestCountDanglingOrganizationProviders(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbService, ctx := setupAuthTestService(t)

	live := models.NewOrganization("acme-one", "acme")
	r.NoError(dbService.CreateOrganization(ctx, live))
	r.NoError(dbService.CreateOrganizationProvider(
		ctx, models.NewOrganizationProvider(live.UID, models.ProviderTypeSlack, "T0ACME0001")))

	// Positive control: a live link must never be counted.
	dangling, err := dbService.CountDanglingOrganizationProviders(ctx)
	r.NoError(err)
	r.Zero(dangling)

	doomed := models.NewOrganization("acmecorp2", "acme")
	r.NoError(dbService.CreateOrganization(ctx, doomed))

	staleLink := models.NewOrganizationProvider(doomed.UID, models.ProviderTypeSlack, "T0ACME0003")
	r.NoError(dbService.CreateOrganizationProvider(ctx, staleLink))

	// The field state: the org is soft-deleted, the link is not released.
	r.NoError(dbService.DeleteOrganization(ctx, doomed.UID))

	dangling, err = dbService.CountDanglingOrganizationProviders(ctx)
	r.NoError(err)
	r.Equal(1, dangling, "a link pointing at a soft-deleted org is dangling")

	// The reporter reads the same number without touching anything: it counts,
	// it does not repair.
	svc.ReportDanglingProviderLinks(ctx)

	stillThere, err := dbService.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, "T0ACME0003")
	r.NoError(err, "reporting must not heal — the healers own that")
	r.Equal(staleLink.UID, stillThere.UID)

	// Clearing it (what a heal does) takes it out of the count.
	r.NoError(dbService.DeleteOrganizationProvider(ctx, staleLink.UID))

	dangling, err = dbService.CountDanglingOrganizationProviders(ctx)
	r.NoError(err)
	r.Zero(dangling, "a cleared link is no longer dangling")
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
				return dropCreatedFlag(github.findOrCreateUser(ctx, &GitHubUserInfo{ID: 4242, Name: "GH", Email: email}))
			},
		},
		{
			name: "gitlab", providerType: models.ProviderTypeGitLab, providerID: "4243",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return dropCreatedFlag(gitlab.findOrCreateUser(ctx, &GitLabUserInfo{ID: 4243, Name: "GL", Email: email}))
			},
		},
		{
			name: "google", providerType: models.ProviderTypeGoogle, providerID: "google-sub-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return dropCreatedFlag(google.findOrCreateUser(ctx, &GoogleUserInfo{
					Sub: "google-sub-1", Email: email, EmailVerified: true, Name: "GO",
				}))
			},
		},
		{
			name: "microsoft", providerType: models.ProviderTypeMicrosoft, providerID: "ms-user-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return dropCreatedFlag(microsoft.findOrCreateUser(
					ctx, &MicrosoftUserInfo{ID: "ms-user-1", DisplayName: "MS"}, email))
			},
		},
		{
			name: "oidc", providerType: models.ProviderTypeOIDC, providerID: "oidc-sub-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return dropCreatedFlag(oidc.findOrCreateUser(ctx, &oidcUserInfo{
					Subject: "oidc-sub-1", Email: email, EmailVerified: true, Name: "OI",
				}))
			},
		},
		{
			name: "saml", providerType: models.ProviderTypeSAML, providerID: "saml-nameid-1",
			signIn: func(ctx context.Context, email string) (*models.User, error) {
				return dropCreatedFlag(saml.findOrCreateUser(ctx, &samlUserInfo{
					NameID: "saml-nameid-1", Email: email, Name: "SA",
				}))
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
			t.Parallel()

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

// dropCreatedFlag adapts a connector's findOrCreateUser — which also reports
// whether it minted the account — to the (*models.User, error) shape this
// table's signIn closures use. The link-resolution behavior under test is the
// same either way.
func dropCreatedFlag(user *models.User, _ bool, err error) (*models.User, error) {
	return user, err
}
