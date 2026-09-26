package oauth

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portClientSecretPG and portClientSecretEnforceOffPG are distinct from every
// other embedded-Postgres port claimed in the repo (including each other,
// since both tests using them run in parallel). See tunnel_postgres_test.go's
// comment for the convention.
const (
	portClientSecretPG           = 15570
	portClientSecretEnforceOffPG = 15571
)

// setupClientSecretFixturePostgres is setupClientSecretFixture backed by a
// real embedded Postgres instead of SQLite — the cross-engine parity guard
// for spec 2026-09-25-27 (client-secret verification is a service-layer
// concern over the same db.Service interface, but the hashing/lookup path is
// exactly the kind of thing that has silently diverged between engines before
// in this codebase, see wiki/testing/test-layers.md). Self-skips under
// -short and on any embedded-startup error, mirroring every other
// embedded-postgres test.
func setupClientSecretFixturePostgres(
	t *testing.T, enforceClientSecret bool, port uint32,
) (oauthFixture, *models.OAuthClient, string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbService, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     port,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbService.Close() })

	if initErr := dbService.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	fullCfg := &config.Config{
		Server: config.ServerConfig{BaseURL: oauthTestIssuer},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		OAuth: config.OAuthConfig{EnforceClientSecret: enforceClientSecret},
	}

	authSvc := auth.NewService(dbService, fullCfg.Auth, fullCfg, nil, nil)
	oauthSvc := NewService(dbService, authSvc, fullCfg)

	org := models.NewOrganization("oauth-secret-pg-org", "")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser("oauth-secret-pg@example.com")
	require.NoError(t, dbService.CreateUser(ctx, user))

	pubClient, _, err := oauthSvc.RegisterClient(
		ctx, "Public Test Client",
		[]string{testRedirectURI},
		[]string{GrantAuthorizationCode, GrantRefreshToken},
		[]string{ScopeMCP}, true,
	)
	require.NoError(t, err)

	confClient, confSecret, err := oauthSvc.RegisterClient(
		ctx, "Confidential Test Client",
		[]string{testRedirectURI},
		[]string{GrantAuthorizationCode, GrantRefreshToken},
		[]string{ScopeMCP}, false,
	)
	require.NoError(t, err)
	require.NotEmpty(t, confSecret)

	f := oauthFixture{
		svc: oauthSvc, authSvc: authSvc, db: dbService,
		org: org, user: user, client: pubClient, ctx: ctx,
	}

	return f, confClient, confSecret
}

// TestTokenEndpointClientSecret_Postgres runs the core client-secret
// verification matrix (correct secret -> issued, wrong/missing -> 401, public
// client unaffected) against a real Postgres backend, mirroring the SQLite
// coverage in client_secret_test.go.
func TestTokenEndpointClientSecret_Postgres(t *testing.T) {
	t.Parallel()

	f, confClient, secret := setupClientSecretFixturePostgres(t, true, portClientSecretPG)
	h := f.tokenHandler()

	t.Run("correct secret issues a token", func(t *testing.T) {
		t.Parallel()

		code := f.issueCodeFor(t, confClient.ClientID)
		rec := postToken(t, h, authCodeForm(code, confClient.ClientID, secret), "", "", false)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("correct secret via Basic issues a token", func(t *testing.T) {
		t.Parallel()

		code := f.issueCodeFor(t, confClient.ClientID)
		form := url.Values{}
		form.Set("grant_type", GrantAuthorizationCode)
		form.Set("code", code)
		form.Set("redirect_uri", testRedirectURI)
		form.Set("code_verifier", testVerifier)
		rec := postToken(t, h, form, confClient.ClientID, secret, true)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("wrong secret is rejected", func(t *testing.T) {
		t.Parallel()

		code := f.issueCodeFor(t, confClient.ClientID)
		rec := postToken(t, h, authCodeForm(code, confClient.ClientID, "wrong-secret"), "", "", false)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("missing secret is rejected", func(t *testing.T) {
		t.Parallel()

		code := f.issueCodeFor(t, confClient.ClientID)
		rec := postToken(t, h, authCodeForm(code, confClient.ClientID, ""), "", "", false)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("public client secret is ignored", func(t *testing.T) {
		t.Parallel()

		code := f.issueCodeFor(t, f.client.ClientID)
		rec := postToken(t, h, authCodeForm(code, f.client.ClientID, "not-checked"), "", "", false)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})
}

// TestTokenEndpointEnforceClientSecretFalse_Postgres pins the escape-hatch
// behavior on Postgres too: a wrong secret still issues a token when
// oauth.enforce_client_secret is false.
func TestTokenEndpointEnforceClientSecretFalse_Postgres(t *testing.T) {
	t.Parallel()

	f, confClient, _ := setupClientSecretFixturePostgres(t, false, portClientSecretEnforceOffPG)
	h := f.tokenHandler()

	code := f.issueCodeFor(t, confClient.ClientID)
	rec := postToken(t, h, authCodeForm(code, confClient.ClientID, "wrong-secret"), "", "", false)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}
