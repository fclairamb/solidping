package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/authhandoff"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// tokenParams are the query parameters the provider callbacks used to put the
// session in. None of them may appear in a callback redirect any more
// (spec 2026-09-25-12).
var tokenParams = []string{"access_token", "refresh_token", "expires_in", "org"} //nolint:gochecknoglobals // test table

// callbackRun is what driving one provider's REAL callback handler produced.
type callbackRun struct {
	rec      *httptest.ResponseRecorder
	db       db.Service
	returnTo string
	// orgSlug is the org the login should land in, or "" to accept whatever
	// the provider resolved (Discord and Slack create their own org).
	orgSlug string
}

// TestProviderCallbacksRedirectWithHandoffCodeOnly drives each provider's real
// callback handler (state validation, token exchange against an httptest
// stand-in, admission policy, session minting) and pins the redirect: the
// dashboard's handoff route with a code, never a token. The code then redeems
// once for the session the callback minted.
func TestProviderCallbacksRedirectWithHandoffCodeOnly(t *testing.T) {
	t.Parallel()

	providers := map[string]func(t *testing.T) callbackRun{
		"google":    runGoogleCallback,
		"github":    runGitHubCallback,
		"gitlab":    runGitLabCallback,
		"microsoft": runMicrosoftCallback,
		"discord":   runDiscordCallback,
		"slack":     runSlackCallback,
		"oidc":      runOIDCCallback,
		"saml":      runSAMLCallback,
	}

	for name, run := range providers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assertHandoffRedirect(t, run(t))
		})
	}
}

// assertHandoffRedirect is the contract every provider callback shares.
func assertHandoffRedirect(t *testing.T, run callbackRun) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	res := run.rec.Result()
	defer func() { _ = res.Body.Close() }()

	r.Equal(http.StatusFound, res.StatusCode, run.rec.Body.String())

	rawLocation := res.Header.Get("Location")
	location, err := url.Parse(rawLocation)
	r.NoError(err)

	r.Equal(handoffCompletePath, location.Path, "the callback must land on the handoff route: %s", rawLocation)

	query := location.Query()
	code := query.Get(handoffCodeParam)
	r.Len(code, 43, "a 32-byte base64url code")

	for _, param := range tokenParams {
		r.False(query.Has(param), "the redirect must not carry %q: %s", param, rawLocation)
	}

	r.False(query.Has(pendingMembershipParam), "an admitted login names no pending org")

	session, err := authhandoff.Redeem(ctx, run.db, code)
	r.NoError(err)
	r.NotEmpty(session.AccessToken)
	r.NotEmpty(session.RefreshToken, "an admitted login hands over a full session")
	r.Positive(session.ExpiresIn)
	r.NotEmpty(session.OrgSlug)
	r.Equal(run.returnTo, session.ReturnTo)

	if run.orgSlug != "" {
		r.Equal(run.orgSlug, session.OrgSlug)
	}

	// The strongest form of "no token in the URL": the minted values
	// themselves appear nowhere in it.
	r.NotContains(rawLocation, session.AccessToken)
	r.NotContains(rawLocation, session.RefreshToken)

	// The SPA cookie still carries the session, as before.
	var cookie *http.Cookie

	for _, c := range res.Cookies() {
		if c.Name == CookieAuthToken {
			cookie = c
		}
	}

	r.NotNil(cookie)
	r.Equal(session.AccessToken, cookie.Value)

	_, err = authhandoff.Redeem(ctx, run.db, code)
	r.ErrorIs(err, authhandoff.ErrInvalidCode, "the code is single use")
}

// serveCallback runs handler against a GET callback request.
func serveCallback(
	t *testing.T, handler func(http.ResponseWriter, *http.Request) error, target string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	require.NoError(t, handler(rec, req))

	return rec
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")

	raw, err := json.Marshal(body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	_, _ = w.Write(raw)
}

func runGoogleCallback(t *testing.T) callbackRun {
	t.Helper()

	svc, ctx := setupGoogleTestService(t)
	org := setupTestOrg(ctx, t, svc)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			writeJSON(w, GoogleTokenResponse{AccessToken: "provider-token", TokenType: "Bearer", ExpiresIn: 3600})

			return
		}

		writeJSON(w, GoogleUserInfo{Sub: "g-1", Email: "member@acme.com", EmailVerified: true, Name: "Member"})
	}))
	t.Cleanup(server.Close)

	svc.tokenURL = server.URL + "/token"
	svc.userInfoURL = server.URL + "/userinfo"
	svc.httpClient = server.Client()

	returnTo := "/d/orgs/" + org.Slug + "/checks"
	state, err := svc.GenerateOAuthState(ctx, returnTo, org.Slug)
	require.NoError(t, err)

	rec := serveCallback(t, NewGoogleOAuthHandler(svc, svc.cfg).Callback,
		"/api/v1/auth/google/callback?code=mock&state="+url.QueryEscape(state))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo, orgSlug: org.Slug}
}

// hostRewriter sends every request to one httptest server, keeping the path:
// the GitHub connector talks to hard-coded github.com / api.github.com URLs.
type hostRewriter struct {
	target *url.URL
	next   http.RoundTripper
}

func (h hostRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = h.target.Scheme
	clone.URL.Host = h.target.Host
	clone.Host = h.target.Host

	return h.next.RoundTrip(clone)
}

func runGitHubCallback(t *testing.T) callbackRun {
	t.Helper()

	svc, ctx := setupGitHubTestService(t)
	org := setupGitHubTestOrg(ctx, t, svc)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			writeJSON(w, GitHubTokenResponse{AccessToken: "provider-token", TokenType: "bearer"})
		case "/user/emails":
			writeJSON(w, []GitHubEmail{{Email: "member@acme.com", Primary: true, Verified: true}})
		case "/user":
			writeJSON(w, GitHubUserInfo{ID: 42, Login: "member", Name: "Member"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	require.NoError(t, err)

	svc.httpClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: hostRewriter{target: target, next: http.DefaultTransport},
	}

	returnTo := "/d/orgs/" + org.Slug + "/checks"
	state, err := svc.GenerateOAuthState(ctx, returnTo, org.Slug)
	require.NoError(t, err)

	rec := serveCallback(t, NewGitHubOAuthHandler(svc, svc.cfg).Callback,
		"/api/v1/auth/github/callback?code=mock&state="+url.QueryEscape(state))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo, orgSlug: org.Slug}
}

func runGitLabCallback(t *testing.T) callbackRun {
	t.Helper()

	svc, ctx := setupGitLabTestService(t)
	org := setupGitLabTestOrg(ctx, t, svc)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			writeJSON(w, GitLabTokenResponse{AccessToken: "provider-token", TokenType: "bearer", ExpiresIn: 3600})
		case "/api/v4/user":
			writeJSON(w, GitLabUserInfo{ID: 7, Username: "member", Name: "Member", Email: "member@acme.com"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	svc.cfg.GitLab.BaseURL = server.URL
	svc.httpClient = server.Client()

	returnTo := "/d/orgs/" + org.Slug + "/checks"
	state, err := svc.GenerateOAuthState(ctx, returnTo, org.Slug)
	require.NoError(t, err)

	rec := serveCallback(t, NewGitLabOAuthHandler(svc, svc.cfg).Callback,
		"/api/v1/auth/gitlab/callback?code=mock&state="+url.QueryEscape(state))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo, orgSlug: org.Slug}
}

func runMicrosoftCallback(t *testing.T) callbackRun {
	t.Helper()

	svc, ctx := setupMicrosoftTestService(t)
	org := setupMicrosoftTestOrg(ctx, t, svc)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			writeJSON(w, MicrosoftTokenResponse{AccessToken: "provider-token", TokenType: "Bearer", ExpiresIn: 3600})

			return
		}

		writeJSON(w, MicrosoftUserInfo{
			ID: "ms-1", DisplayName: "Member", Mail: "member@acme.com", UserPrincipalName: "member@acme.com",
		})
	}))
	t.Cleanup(server.Close)

	svc.tokenURL = server.URL + "/token"
	svc.userURL = server.URL + "/me"
	svc.httpClient = server.Client()

	returnTo := "/d/orgs/" + org.Slug + "/checks"
	state, err := svc.GenerateOAuthState(ctx, returnTo, org.Slug)
	require.NoError(t, err)

	rec := serveCallback(t, NewMicrosoftOAuthHandler(svc, svc.cfg).Callback,
		"/api/v1/auth/microsoft/callback?code=mock&state="+url.QueryEscape(state))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo, orgSlug: org.Slug}
}

func runDiscordCallback(t *testing.T) callbackRun {
	t.Helper()

	svc := newDiscordTestService(t, newSQLiteDBService(t))
	fakeDiscordEndpoints(t, svc, discordTestUser(nextFixture()), nil)

	// Discord's redirect_uri defaults to "/"; any value round-trips as-is.
	returnTo := "/d/orgs/anything"
	state, err := svc.GenerateOAuthState(t.Context(), returnTo)
	require.NoError(t, err)

	rec := serveCallback(t, NewDiscordOAuthHandler(svc, svc.cfg).Callback,
		"/api/v1/auth/discord/callback?code=mock&state="+url.QueryEscape(state))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo}
}

func runSlackCallback(t *testing.T) callbackRun {
	t.Helper()

	svc, ctx := setupSlackTestService(t)
	fakeSlackEndpoints(t, svc, "T-HANDOFF", "Acme", "member@acme.com")

	returnTo := "/d/orgs/anything"
	state, err := svc.GenerateOAuthState(ctx, returnTo)
	require.NoError(t, err)

	rec := serveCallback(t, NewSlackOAuthHandler(svc, svc.cfg).Callback,
		"/api/v1/auth/slack/callback?code=mock&state="+url.QueryEscape(state))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo}
}

func runOIDCCallback(t *testing.T) callbackRun {
	t.Helper()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	org := setupOIDCTestOrg(ctx, t, svc)

	returnTo := "/d/orgs/" + org.Slug + "/checks"
	state, err := svc.GenerateOAuthState(ctx, returnTo, org.Slug)
	require.NoError(t, err)

	idp.nextIDToken = idp.issueIDToken(t, nil)

	rec := serveCallback(t, NewOIDCOAuthHandler(svc, svc.cfg).Callback,
		"/api/v1/auth/oidc/callback?code=mock&state="+url.QueryEscape(state))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo, orgSlug: org.Slug}
}

func runSAMLCallback(t *testing.T) callbackRun {
	t.Helper()

	r := require.New(t)
	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	returnTo := "/d/orgs/" + org.Slug + "/checks"
	redirectURL, err := svc.GenerateAuthnRequest(ctx, returnTo, org.Slug)
	r.NoError(err)

	parsed, err := url.Parse(redirectURL)
	r.NoError(err)

	relayState := parsed.Query().Get("RelayState")
	r.NotEmpty(relayState)

	// Peek (without consuming it — the handler does that) at the request ID
	// the assertion must answer.
	entry, err := svc.db.GetStateEntry(ctx, nil, samlStatePrefix+relayState)
	r.NoError(err)

	rawState, ok := (*entry.Value)[keyState].(string)
	r.True(ok)

	var stored SAMLRelayState
	r.NoError(json.Unmarshal([]byte(rawState), &stored))

	spClient, err := svc.serviceProvider(ctx)
	r.NoError(err)

	req := buildSignedAssertionRequest(t, idp, spClient.Metadata(), stored.RequestID,
		defaultTestSession("member@acme.com", "Member"), time.Now(), svc.acsURL())
	req.PostForm.Set("RelayState", relayState)

	rec := httptest.NewRecorder()
	r.NoError(NewSAMLHandler(svc, svc.cfg).ACS(rec, req))

	return callbackRun{rec: rec, db: svc.db, returnTo: returnTo, orgSlug: org.Slug}
}

// TestPendingCallbackRedirectsWithHandoffCode covers the org-less outcome of
// the shared tail: a login the org did not admit gets the same handoff route,
// with the non-secret membershipPending flag when a request was opened, and a
// code that redeems for an org-less session.
func TestPendingCallbackRedirectsWithHandoffCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pendingOrgSlug string
	}{
		{"named org", "acme"},
		// Rule 6's suppressed request: no org may be named, so the
		// "a join request was sent" alert never renders.
		{"no org to name", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			svc, dbSvc, ctx := setupAuthTestService(t)
			user := joinTestUser(ctx, t, dbSvc, "outsider@elsewhere.example")

			pending, err := svc.pendingSession(ctx, user)
			r.NoError(err)

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/google/callback", nil)

			r.NoError(finishProviderCallback(rec, req, dbSvc, "google", "/d/orgs/acme", &ProviderOutcome{
				AccessToken:    pending.AccessToken,
				ExpiresIn:      pending.ExpiresIn,
				OrgSlug:        "acme",
				UserUID:        user.UID,
				Pending:        true,
				PendingOrgSlug: tc.pendingOrgSlug,
			}))

			r.Equal(http.StatusFound, rec.Code)

			rawLocation := rec.Header().Get("Location")
			location, err := url.Parse(rawLocation)
			r.NoError(err)
			r.Equal(handoffCompletePath, location.Path)
			r.NotContains(rawLocation, pending.AccessToken)

			query := location.Query()
			for _, param := range tokenParams {
				r.False(query.Has(param), "the redirect must not carry %q", param)
			}

			if tc.pendingOrgSlug == "" {
				r.False(query.Has(pendingMembershipParam))
			} else {
				r.Equal(tc.pendingOrgSlug, query.Get(pendingMembershipParam))
			}

			session, err := authhandoff.Redeem(ctx, dbSvc, query.Get(handoffCodeParam))
			r.NoError(err)
			r.Equal(pending.AccessToken, session.AccessToken)
			r.Empty(session.OrgSlug, "a pending login hands over an org-less session")
			r.Empty(session.RefreshToken)
			r.Equal(tc.pendingOrgSlug, session.MembershipPending)
		})
	}
}

// TestFinishProviderCallbackStorageFailure: when the handoff cannot be stored
// the browser goes back where it came from with the generic error, and no
// session cookie is set.
func TestFinishProviderCallbackStorageFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, dbSvc, ctx := setupAuthTestService(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/google/callback", nil)

	// No such user: the row's foreign key refuses it.
	r.NoError(finishProviderCallback(rec, req, dbSvc, "google", "/d/orgs/acme/login", &ProviderOutcome{
		AccessToken: "at-secret", RefreshToken: "rt-secret", ExpiresIn: 3600,
		OrgSlug: "does-not-exist", UserUID: models.NewUser("ghost@acme.com").UID,
	}))

	r.Equal(http.StatusFound, rec.Code)

	location, err := url.Parse(rec.Header().Get("Location"))
	r.NoError(err)
	r.Equal("/d/orgs/acme/login", location.Path)
	r.Equal(OAuthCodeFailed, location.Query().Get("error"))
	r.NotContains(rec.Header().Get("Location"), "secret")
	r.Empty(rec.Result().Cookies())
}

// TestHandoffRedirectShape pins the URL the dashboard route parses.
func TestHandoffRedirectShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("/d/auth/complete?code=abc", handoffRedirect("abc", ""))
	r.Equal("/d/auth/complete?code=abc&membershipPending=acme", handoffRedirect("abc", "acme"))
}
