package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOIDCCallback_SetsAccessTokenCookie is the regression test for the SSO
// callbacks never setting the access_token cookie: the cookie is what the
// embedded MCP OAuth authorize/consent flow authenticates with, so without it
// SSO users bounced through the login page before reaching the consent
// screen. The OIDC provider is the one whose real HandleCallback runs
// end-to-end against the in-repo fake IdP, so it stands in for all eight
// SSO callbacks (they share the same success-redirect pattern).
func TestOIDCCallback_SetsAccessTokenCookie(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	org := setupOIDCTestOrg(ctx, t, svc)
	handler := NewOIDCOAuthHandler(svc, svc.cfg)

	state, err := svc.GenerateOAuthState(ctx, "/dash0/orgs/"+org.Slug, org.Slug)
	r.NoError(err)

	idp.nextIDToken = idp.issueIDToken(t, nil)

	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/api/v1/auth/oidc/callback?code=fake-code&state="+url.QueryEscape(state), nil)
	rec := httptest.NewRecorder()

	r.NoError(handler.Callback(rec, req))

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	// Existing behavior: 302 with tokens in the redirect URL query.
	r.Equal(http.StatusFound, res.StatusCode)
	location, err := url.Parse(res.Header.Get("Location"))
	r.NoError(err)

	accessToken := location.Query().Get("access_token")
	r.NotEmpty(accessToken)

	// New behavior: the redirect response also carries the SPA session
	// cookie, mirroring what the password Login handler sets.
	var authCookie *http.Cookie

	for _, c := range res.Cookies() {
		if c.Name == CookieAuthToken {
			authCookie = c
		}
	}

	r.NotNil(authCookie, "callback response must set the %s cookie", CookieAuthToken)
	r.Equal(accessToken, authCookie.Value)
	r.Equal("/", authCookie.Path)
	r.Positive(authCookie.MaxAge)
}

// TestOIDCCallback_ErrorRedirectSetsNoCookie pins the error path: only the
// success redirect may set the session cookie.
func TestOIDCCallback_ErrorRedirectSetsNoCookie(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	handler := NewOIDCOAuthHandler(svc, svc.cfg)

	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/api/v1/auth/oidc/callback?code=fake-code&state=bogus-state", nil)
	rec := httptest.NewRecorder()

	r.NoError(handler.Callback(rec, req))

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	r.Equal(http.StatusFound, res.StatusCode)
	location, err := url.Parse(res.Header.Get("Location"))
	r.NoError(err)
	r.NotEmpty(location.Query().Get("error"))

	for _, c := range res.Cookies() {
		r.NotEqual(CookieAuthToken, c.Name, "error redirect must not set the session cookie")
	}
}
