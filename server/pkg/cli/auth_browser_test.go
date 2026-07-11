package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/oauth"
)

// TestGeneratePKCE checks the verifier is within the RFC 7636 length bounds and
// that the emitted challenge is the S256 hash the server will verify against.
func TestGeneratePKCE(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	verifier, challenge, err := generatePKCE()
	r.NoError(err)
	r.GreaterOrEqual(len(verifier), 43)
	r.LessOrEqual(len(verifier), 128)
	r.NotEmpty(challenge)

	// The server verifies the exchange with oauth.VerifyPKCE — our pair must pass.
	r.True(oauth.VerifyPKCE(verifier, challenge))

	// A different verifier must not satisfy the same challenge.
	other, _, err := generatePKCE()
	r.NoError(err)
	r.False(oauth.VerifyPKCE(other, challenge))
}

// TestGenerateState checks state is non-empty and fresh on each call.
func TestGenerateState(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	first, err := generateState()
	r.NoError(err)
	r.NotEmpty(first)

	second, err := generateState()
	r.NoError(err)
	r.NotEqual(first, second)
}

// TestBuildAuthorizeURL pins the authorization request the CLI sends: the
// pre-registered client, a code response, S256 PKCE, and the loopback redirect.
func TestBuildAuthorizeURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	redirectURI := "http://127.0.0.1:54321/callback"
	raw := buildAuthorizeURL("https://solidping.test", redirectURI, "the-challenge", "the-state")

	parsed, err := url.Parse(raw)
	r.NoError(err)
	r.Equal("/api/v1/oauth/authorize", parsed.Path)

	query := parsed.Query()
	r.Equal(oauth.CLIClientID, query.Get("client_id"))
	r.Equal(oauth.ResponseTypeCode, query.Get("response_type"))
	r.Equal(oauth.CodeChallengeMethodS256, query.Get("code_challenge_method"))
	r.Equal("the-challenge", query.Get("code_challenge"))
	r.Equal("the-state", query.Get("state"))
	r.Equal(redirectURI, query.Get("redirect_uri"))
	// scope/resource are omitted so the server applies its defaults.
	r.Empty(query.Get("scope"))
}

// callGET drives the callback handler with a raw callback URL and returns the
// recorder plus the delivered result.
func callGET(t *testing.T, callback *loopbackCallback, rawURL string) (*httptest.ResponseRecorder, callbackResult) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, rawURL, http.NoBody)
	rec := httptest.NewRecorder()
	callback.handler().ServeHTTP(rec, req)

	return rec, <-callback.resultCh
}

func newCallback(state string) *loopbackCallback {
	return &loopbackCallback{state: state, resultCh: make(chan callbackResult, 1)}
}

// TestLoopbackCallbackSuccess: matching state + code yields the code and a 200
// close-tab page.
func TestLoopbackCallbackSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	callback := newCallback("expected-state")
	rec, res := callGET(t, callback, "/callback?code=the-code&state=expected-state")

	r.NoError(res.err)
	r.Equal("the-code", res.code)
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(strings.ToLower(rec.Body.String()), "successful")
}

// TestLoopbackCallbackStateMismatch: a wrong state is rejected as a possible CSRF.
func TestLoopbackCallbackStateMismatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	callback := newCallback("expected-state")
	rec, res := callGET(t, callback, "/callback?code=the-code&state=attacker-state")

	r.ErrorIs(res.err, errStateMismatch)
	r.Empty(res.code)
	r.Equal(http.StatusBadRequest, rec.Code)
}

// TestLoopbackCallbackMissingCode: a callback with no code is an error.
func TestLoopbackCallbackMissingCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	callback := newCallback("expected-state")
	rec, res := callGET(t, callback, "/callback?state=expected-state")

	r.ErrorIs(res.err, errNoAuthCode)
	r.Equal(http.StatusBadRequest, rec.Code)
}

// TestLoopbackCallbackOAuthError: an error redirect (e.g. denied consent) is
// surfaced.
func TestLoopbackCallbackOAuthError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	callback := newCallback("expected-state")
	rec, res := callGET(t, callback,
		"/callback?error=access_denied&error_description=user+said+no&state=expected-state")

	r.ErrorIs(res.err, errAuthorizationDenied)
	r.Equal(http.StatusBadRequest, rec.Code)
}

// TestLoopbackCallbackDeliversOnce: a stray second request (e.g. a favicon or
// reload) must not block or panic — finish delivers exactly once.
func TestLoopbackCallbackDeliversOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	callback := newCallback("expected-state")

	rec1, res := callGET(t, callback, "/callback?code=the-code&state=expected-state")
	r.Equal(http.StatusOK, rec1.Code)
	r.Equal("the-code", res.code)

	// A second hit does not deliver again (channel already drained, buffer 1).
	req := httptest.NewRequest(http.MethodGet, "/callback?code=other&state=expected-state", http.NoBody)
	rec2 := httptest.NewRecorder()
	callback.handler().ServeHTTP(rec2, req)

	select {
	case extra := <-callback.resultCh:
		r.Failf("unexpected second delivery", "got %+v", extra)
	default:
	}
}
