package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// oidcWorld is the OIDC service wired to the fake IdP, with one organization.
// It is shared by the subtests of a test: every sign-in uses its own subject
// and address, so they do not see each other's accounts.
type oidcWorld struct {
	svc   *OIDCOAuthService
	ctx   context.Context //nolint:containedctx // test fixture
	idp   *fakeOIDCIdP
	org   *models.Organization
	fault *queryFault
	seq   int
}

func newOIDCWorld(t *testing.T) *oidcWorld {
	t.Helper()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)

	return &oidcWorld{svc: svc, ctx: ctx, idp: idp, org: setupOIDCTestOrg(ctx, t, svc), fault: installQueryFault(svc.db)}
}

// identity makes the IdP answer the next sign-ins with a fresh identity,
// adjusted by mutate, and returns its address.
func (w *oidcWorld) identity(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()

	w.seq++
	email := fmt.Sprintf("member%d@acme.com", w.seq)

	w.idp.nextIDToken = w.idp.issueIDToken(t, func(claims jwt.MapClaims) {
		claims["sub"] = fmt.Sprintf("oidc-sub-%d", w.seq)
		claims[keyEmail] = email

		if mutate != nil {
			mutate(claims)
		}
	})

	return email
}

func (w *oidcWorld) callback() error {
	_, err := w.svc.HandleCallback(w.ctx, "code", w.org.Slug)

	return err
}

func (w *oidcWorld) account(email string) *models.User {
	user, err := w.svc.db.GetUserByEmail(w.ctx, email)
	if err != nil {
		return nil
	}

	return user
}

// TestOIDCCallbackRefusals: what the IdP answers decides whether an account
// may be created.
func TestOIDCCallbackRefusals(t *testing.T) {
	t.Parallel()

	w := newOIDCWorld(t)

	// Token response without an ID token.
	{
		w.idp.nextIDToken = ""

		require.ErrorIs(t, w.callback(), ErrOIDCNoIDToken)
	}

	// ID token without an email.
	{
		email := w.identity(t, func(claims jwt.MapClaims) { delete(claims, keyEmail) })

		require.ErrorIs(t, w.callback(), ErrEmailNotVerified)
		require.Nil(t, w.account(email))
	}

	// ID token without a name: the address is the name.
	{
		email := w.identity(t, func(claims jwt.MapClaims) { delete(claims, keyName) })

		require.NoError(t, w.callback())
		require.Equal(t, email, w.account(email).Name)
	}

	// Token endpoint down after discovery.
	{
		email := w.identity(t, nil)
		healthy := w.svc.httpClient
		w.svc.httpClient = failingHTTPClient()

		err := w.callback()
		w.svc.httpClient = healthy

		require.ErrorIs(t, err, ErrOIDCTokenExchange)
		require.ErrorIs(t, err, errProviderDown)
		require.Nil(t, w.account(email))
	}
}

// TestOIDCCallbackWhenTheStoreFails mirrors
// TestProviderCallbackWhenTheStoreFails for the OIDC provider.
func TestOIDCCallbackWhenTheStoreFails(t *testing.T) { //nolint:tparallel // subtests share one IdP
	t.Parallel()

	w := newOIDCWorld(t)

	steps := []struct {
		name    string
		fault   sqlFault
		wantMsg string
	}{
		{"reading the organization", failingSQL(`FROM "organizations"`), "organization not found"},
		{"looking the provider link up", failingSQL(`FROM "user_providers"`), "failed to get user provider"},
		{"looking the account up by email", failingSQL(`FROM "users"`), "failed to get user by email"},
		{"creating the account", failingSQL(`INSERT INTO "users"`), "failed to create user"},
		{"creating the provider link", failingSQL(`INSERT INTO "user_providers"`), "failed to create user provider"},
	}
	for _, st := range steps { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(st.name, func(t *testing.T) {
			email := w.identity(t, nil)

			var err error

			w.fault.during(t, st.fault, func() { err = w.callback() })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, st.wantMsg)

			require.NoError(t, w.callback(), "the sign-in works once the store is back")
			require.NotNil(t, w.account(email))
		})
	}

	// Reading the account behind an existing link.
	{
		w.identity(t, nil)
		require.NoError(t, w.callback())

		var err error

		w.fault.during(t, failingSQL(`FROM "users"`), func() { err = w.callback() })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to get user behind provider link")
	}
}

func TestOIDCState(t *testing.T) { //nolint:tparallel // subtests share one IdP
	t.Parallel()

	w := newOIDCWorld(t)

	runOAuthStateRefusals(t, oauthStateStore{
		"oidc", oidcOAuthStatePrefix, w.svc.db,
		func() (string, error) { return w.svc.GenerateOAuthState(w.ctx, "/d", w.org.Slug) },
		func(nonce string) (*OAuthState, error) { return w.svc.ValidateOAuthState(w.ctx, nonce) },
	})
}

func TestOIDCScopesAlwaysIncludeOpenID(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"":                {"openid", "email", "profile"},
		"email groups":    {"openid", "email", "groups"},
		"groups openid":   {"groups", "openid"},
		"  openid\temail": {"openid", "email"},
	}
	for configured, want := range tests {
		svc := &OIDCOAuthService{cfg: &config.Config{OIDC: config.OIDCOAuthConfig{Scopes: configured}}}
		require.Equal(t, want, svc.scopes(), "configured %q", configured)
	}
}

// TestOIDCLoginRoute covers the answers of GET /auth/oidc/login that are not
// a redirect to the IdP.
func TestOIDCLoginRoute(t *testing.T) {
	t.Parallel()

	w := newOIDCWorld(t)
	handler := NewOIDCOAuthHandler(w.svc, w.svc.cfg)
	target := "/api/v1/auth/oidc/login?org=" + w.org.Slug

	// Unknown organization.
	{
		_, code, _ := errorRedirect(t, serveRoute(t, handler.Login, "/api/v1/auth/oidc/login?org=no-such-org"))
		require.Equal(t, string(base.ErrorCodeOrganizationNotFound), code)
	}

	// State cannot be stored.
	{
		var rec *httptest.ResponseRecorder

		w.fault.during(t, failingSQL(`"state_entries"`), func() { rec = serveRoute(t, handler.Login, target) })
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Empty(t, rec.Header().Get("Location"))
	}

	// Control: redirects to the IdP.
	{
		rec := serveRoute(t, handler.Login, target)
		require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
		require.Contains(t, rec.Header().Get("Location"), w.idp.server.URL+"/authorize")
	}

	// Callback denied at the IdP.
	{
		_, code, _ := errorRedirect(t, serveRoute(t, handler.Callback, "/api/v1/auth/oidc/callback?error=access_denied"))
		require.Equal(t, "access_denied", code)
	}

	// Callback without its parameters.
	{
		_, code, _ := errorRedirect(t, serveRoute(t, handler.Callback, "/api/v1/auth/oidc/callback?state=abc"))
		require.Equal(t, "INVALID_REQUEST", code)
	}

	// The two cases below change the configuration, so they run last.
	// Issuer that does not answer discovery.
	{
		w.svc.cfg.OIDC.IssuerURL = w.idp.server.URL + "/no-such-issuer"

		rec := serveRoute(t, handler.Login, target)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Equal(t, string(base.ErrorCodeInternalError), errorBody(t, rec)["code"])
	}

	// Provider switched off.
	{
		w.svc.cfg.OIDC.Enabled = false

		rec := serveRoute(t, handler.Login, target)
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, string(base.ErrorCodeNotFound), errorBody(t, rec)["code"])

		require.ErrorIs(t, w.callback(), ErrOIDCNotConfigured)
	}
}
