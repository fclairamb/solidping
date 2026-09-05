package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
)

func setupSlackService(t *testing.T) (context.Context, *Service) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
		Slack:  config.SlackConfig{ClientID: "test-client-id", ClientSecret: "test-client-secret"},
	}

	svc := NewService(dbService, cfg, nil, nil, nil)

	return ctx, svc
}

func TestBuildInstallURL_GeneratesValidStateAndScopes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	authorizeURL, err := svc.BuildInstallURL(ctx, "marketplace", "", "")
	r.NoError(err)

	r.True(strings.HasPrefix(authorizeURL, "https://slack.com/oauth/v2/authorize?"))
	r.Contains(authorizeURL, "client_id=test-client-id")
	r.Contains(authorizeURL, "redirect_uri=http%3A%2F%2Flocalhost%3A4000%2Fapi%2Fv1%2Fintegrations%2Fslack%2Foauth")
	r.Contains(authorizeURL, "scope=chat%3Awrite")
	r.Contains(authorizeURL, "user_scope=openid%2Cemail%2Cprofile")
	r.Contains(authorizeURL, "state=")

	// State should be redeemable as a slack-install kind, and the source
	// payload from the request should round-trip.
	stateValue := extractStateParam(t, authorizeURL)
	entry, err := oauthstate.Validate(ctx, svc.db, "slack-install", stateValue)
	r.NoError(err)
	r.Equal("marketplace", entry.Payload["source"])
}

func TestHandleOAuthCallback_RejectsMissingState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	_, err := svc.HandleOAuthCallback(ctx, "any-code", "")
	r.ErrorIs(err, ErrInvalidState)
}

func TestHandleOAuthCallback_RejectsUnknownState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	_, err := svc.HandleOAuthCallback(ctx, "any-code", "fabricated-nonce")
	r.ErrorIs(err, ErrInvalidState)
}

func TestHandleOAuthCallback_RejectsSignInState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	// A sign-in state cannot be redeemed by the install callback.
	signInNonce, err := oauthstate.Generate(ctx, svc.db, "slack-signin", nil, time.Minute)
	r.NoError(err)

	_, err = svc.HandleOAuthCallback(ctx, "any-code", signInNonce)
	r.ErrorIs(err, ErrInvalidState)
}

func TestHandleOAuthCallback_StateConsumedOnReuse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	// We can't run a full successful callback (requires mocking Slack),
	// but a successful state validation must consume the entry. The
	// callback returns a non-state error after the state passes (the
	// Slack token exchange will fail in tests), proving the state was
	// accepted, and a second call with the same state must fail with
	// ErrInvalidState.
	authorizeURL, err := svc.BuildInstallURL(ctx, "", "", "")
	r.NoError(err)

	stateValue := extractStateParam(t, authorizeURL)

	// First call: state is consumed; the call fails on the Slack token
	// exchange (no real network), surfacing as ErrOAuthFailed.
	_, firstErr := svc.HandleOAuthCallback(ctx, "fake-code", stateValue)
	r.Error(firstErr)
	r.NotErrorIs(firstErr, ErrInvalidState)

	// Second call with the same state: ErrInvalidState (state is gone).
	_, secondErr := svc.HandleOAuthCallback(ctx, "fake-code", stateValue)
	r.ErrorIs(secondErr, ErrInvalidState)
}

func TestIssueExchangeCode_RoundTripIsSingleUse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	result := &OAuthResult{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		OrgSlug:      "acme",
		UserUID:      "user-uid",
	}

	code, err := svc.IssueExchangeCode(ctx, result)
	r.NoError(err)
	r.NotEmpty(code)

	entry, err := oauthstate.Validate(ctx, svc.db, "slack-exchange", code)
	r.NoError(err)
	r.Equal("access-1", entry.Payload["accessToken"])
	r.Equal("refresh-1", entry.Payload["refreshToken"])
	r.Equal("acme", entry.Payload["orgSlug"])
	r.Equal("user-uid", entry.Payload["userUID"])

	// Reuse must fail.
	_, err = oauthstate.Validate(ctx, svc.db, "slack-exchange", code)
	r.ErrorIs(err, oauthstate.ErrInvalidState)
}

// extractStateParam pulls the CSRF state nonce out of a Slack authorize URL.
func extractStateParam(t *testing.T, urlString string) string {
	t.Helper()

	const key = "state"

	idx := strings.Index(urlString, "?")
	require.NotEqual(t, -1, idx, "url has no query string: %s", urlString)

	for _, pair := range strings.Split(urlString[idx+1:], "&") {
		eq := strings.Index(pair, "=")
		if eq == -1 {
			continue
		}

		if pair[:eq] == key {
			return pair[eq+1:]
		}
	}

	t.Fatalf("query param %q not found in %s", key, urlString)

	return ""
}

// TestHandleOAuthCallback_EchoesAuthorizeRedirectURIAtExchange is the
// regression test for the install flow dying with `bad_redirect_uri`.
//
// Slack's oauth.v2.access requires the exchange to echo the SAME
// redirect_uri the authorize request carried. BuildInstallURL always sent
// one; exchangeCodeAndFetchUser passed "" with a comment claiming it was
// optional. Every install started from our own OAuth URL therefore failed
// after the user clicked Allow, redirecting to
// /saas/install-error?reason=oauth_failed — while Slack *login*, which
// passes the callback URL at both ends, kept working and masked it.
//
// Nothing caught it because no test ever drove a SUCCESSFUL exchange: the
// only other test through this path (StateConsumedOnReuse) deliberately
// relies on the exchange failing. So this test asserts the exchange request
// itself, not just the authorize URL.
func TestHandleOAuthCallback_EchoesAuthorizeRedirectURIAtExchange(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	var (
		exchangeForm url.Values
		parseErr     error
	)

	tokenServer := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, req *http.Request) {
			// Not require.* — this runs on the server goroutine, where a
			// FailNow would be unsafe. Captured and asserted below instead;
			// the HTTP round trip completes before HandleOAuthCallback
			// returns, so reading these afterwards is ordered.
			parseErr = req.ParseForm()
			exchangeForm = req.PostForm

			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true,"access_token":"xoxb-1",` +
				`"team":{"id":"T1","name":"Acme"},` +
				`"authed_user":{"id":"U1","access_token":"xoxp-1"}}`))
		}))
	t.Cleanup(tokenServer.Close)

	// Stubbed so the test never touches the network. An empty email stops
	// the callback with ErrEmailRequired immediately AFTER the exchange —
	// which is precisely the point: reaching it proves the exchange was
	// accepted.
	userInfoServer := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true,"email":""}`))
		}))
	t.Cleanup(userInfoServer.Close)

	svc.oauthURL = tokenServer.URL
	svc.userInfoURL = userInfoServer.URL

	authorizeURL, err := svc.BuildInstallURL(ctx, "dashboard", "", "")
	r.NoError(err)

	parsed, err := url.Parse(authorizeURL)
	r.NoError(err)

	authorizeRedirectURI := parsed.Query().Get("redirect_uri")
	r.NotEmpty(authorizeRedirectURI, "authorize must carry a redirect_uri")

	_, err = svc.HandleOAuthCallback(ctx, "fake-code", extractStateParam(t, authorizeURL))

	// Past the exchange: the exchange itself succeeded, and we stopped on
	// the stubbed empty email. A bad_redirect_uri would surface as
	// ErrOAuthFailed instead.
	r.ErrorIs(err, ErrEmailRequired)
	r.NotErrorIs(err, ErrOAuthFailed)
	r.NoError(parseErr)

	// The assertion that matters: the exchange echoed the authorize value.
	r.Equal(authorizeRedirectURI, exchangeForm.Get("redirect_uri"),
		"exchange redirect_uri must be byte-identical to the authorize one, "+
			"or Slack answers bad_redirect_uri")

	// Positive control: pin the concrete value, so a helper that silently
	// returned "" on both sides would still fail here.
	r.Equal("http://localhost:4000/api/v1/integrations/slack/oauth",
		exchangeForm.Get("redirect_uri"))
}
