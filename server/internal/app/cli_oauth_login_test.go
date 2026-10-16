package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/oauth"
)

// TestCLIBrowserLoginWalkthrough drives the exact server-side sequence the
// legacy loopback `sp auth login` (spec 2026-07-11-08) performs: authorize
// (GET → consent) → approve (POST → code) → token exchange → mint a PAT with
// the OAuth access token → use the PAT.
//
// RequireAuth refuses MCP credentials on REST (spec 2026-10-16-12); the PAT
// mint works through the CLI client exception, which only the first-party
// `solidping-cli` client with the full `mcp` scope gets.
func TestCLIBrowserLoginWalkthrough(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newMCPTestEnv(t)

	// The CLI binds an ephemeral loopback listener; the registered redirect
	// ignores the port, so any 127.0.0.1:<port>/callback is accepted.
	redirectURI := "http://127.0.0.1:19123" + oauth.CLICallbackPath
	accessToken := loopbackOAuthAccessToken(t, env, oauth.CLIClientID, redirectURI)

	// --- Mint a PAT with the OAuth access token: the CLI client exception.
	res := mintPATWithBearer(t, env, accessToken)
	r.Equal(http.StatusCreated, res.status, "mint PAT: %s", res.body)

	var pat struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	r.NoError(json.Unmarshal(res.body, &pat))
	r.True(strings.HasPrefix(pat.Token, "pat_"), "expected a pat_ token, got %q", pat.Token)
	r.Equal("sp CLI on test-host", pat.Name)

	// --- The saved PAT authenticates a real org-scoped call.
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet,
		env.ts.URL+"/api/v1/orgs/"+mcpE2EOrg+"/checks", nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+pat.Token)
	res = env.do(t, req)
	r.Equal(http.StatusOK, res.status, "PAT call: %s", res.body)

	// --- The OAuth access token itself still reads nothing on REST.
	res = env.get(t, "/api/v1/orgs/"+mcpE2EOrg+"/checks",
		map[string]string{"Authorization": "Bearer " + accessToken})
	r.Equal(http.StatusUnauthorized, res.status, "CLI OAuth token on REST: %s", res.body)
}

// TestDynamicClientCannotMintPAT is the twin of TestCLIBrowserLoginWalkthrough
// with a dynamically registered client (spec 2026-10-16-12): the same flow
// ends with a 401 on the PAT mint, since the CLI exception is keyed on the
// first-party client ID that no registration can claim.
func TestDynamicClientCannotMintPAT(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newMCPTestEnv(t)

	redirectURI := "http://127.0.0.1:19124/callback"
	regBody := fmt.Sprintf(
		`{"client_name":"third-party","redirect_uris":[%q],`+
			`"grant_types":["authorization_code","refresh_token"],`+
			`"token_endpoint_auth_method":"none"}`,
		redirectURI)
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, env.ts.URL+"/api/v1/oauth/register", strings.NewReader(regBody))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	res := env.do(t, req)
	r.Equal(http.StatusCreated, res.status, "register: %s", res.body)

	//nolint:tagliatelle // RFC 7591 wire format requires snake_case field names.
	var reg struct {
		ClientID string `json:"client_id"`
	}
	r.NoError(json.Unmarshal(res.body, &reg))
	r.NotEmpty(reg.ClientID)

	accessToken := loopbackOAuthAccessToken(t, env, reg.ClientID, redirectURI)

	res = mintPATWithBearer(t, env, accessToken)
	r.Equal(http.StatusUnauthorized, res.status, "mint PAT: %s", res.body)
	r.Contains(string(res.body), "INVALID_TOKEN")
}

// loopbackOAuthAccessToken runs authorize (GET → consent) → approve (POST →
// code) → PKCE token exchange for clientID, with a logged-in session, and
// returns the OAuth access token. Scope and resource are omitted, as the CLI
// does: the server defaults them to the full `mcp` scope.
func loopbackOAuthAccessToken(t *testing.T, env *mcpTestEnv, clientID, redirectURI string) string {
	t.Helper()
	r := require.New(t)

	sessionToken := env.login(t)

	verifier := "cli-walkthrough-code-verifier-0123456789abcdefghij"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	authQuery := url.Values{}
	authQuery.Set("client_id", clientID)
	authQuery.Set("redirect_uri", redirectURI)
	authQuery.Set("response_type", "code")
	authQuery.Set("state", "cli-walkthrough-state")
	authQuery.Set("code_challenge", challenge)
	authQuery.Set("code_challenge_method", "S256")

	// --- Authorize (GET): a logged-in session bounces to the consent screen.
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet,
		env.ts.URL+"/api/v1/oauth/authorize?"+authQuery.Encode(), nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	res := env.do(t, req)
	r.Equal(http.StatusFound, res.status)
	r.Contains(res.header.Get("Location"), "/oauth/consent?")

	// --- Approve (POST): consent mints the one-time code on the redirect.
	req, err = http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		env.ts.URL+"/api/v1/oauth/authorize", strings.NewReader(authQuery.Encode()))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	res = env.do(t, req)
	r.Equal(http.StatusFound, res.status, "approve: %s", res.body)

	loc, err := url.Parse(res.header.Get("Location"))
	r.NoError(err)
	r.True(strings.HasPrefix(res.header.Get("Location"), redirectURI))
	code := loc.Query().Get("code")
	r.NotEmpty(code)
	r.Equal("cli-walkthrough-state", loc.Query().Get("state"))

	// --- Token exchange (PKCE).
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("code", code)
	tokenForm.Set("client_id", clientID)
	tokenForm.Set("redirect_uri", redirectURI)
	tokenForm.Set("code_verifier", verifier)

	req, err = http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		env.ts.URL+"/api/v1/oauth/token", strings.NewReader(tokenForm.Encode()))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res = env.do(t, req)
	r.Equal(http.StatusOK, res.status, "token: %s", res.body)

	//nolint:tagliatelle // RFC 6749 wire format requires snake_case field names.
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	r.NoError(json.Unmarshal(res.body, &tok))
	r.NotEmpty(tok.AccessToken)
	// The credential on the wire is a JWT (never the PAT) — only the code
	// transited the loopback redirect.
	r.True(strings.HasPrefix(tok.AccessToken, "eyJ"))

	return tok.AccessToken
}

// mintPATWithBearer calls POST /orgs/{org}/tokens with the given bearer.
func mintPATWithBearer(t *testing.T, env *mcpTestEnv, bearer string) httpResult {
	t.Helper()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		env.ts.URL+"/api/v1/orgs/"+mcpE2EOrg+"/tokens",
		strings.NewReader(`{"name":"sp CLI on test-host"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)

	return env.do(t, req)
}

// TestCLIOAuthClientSeededAtStartup confirms the seed ran in the full-server
// boot path used by the harness, with the loopback shape the CLI needs.
func TestCLIOAuthClientSeededAtStartup(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newMCPTestEnv(t)

	// Discovery still works, and an /authorize for the seeded client with no
	// session bounces to login rather than rejecting an unknown client.
	authQuery := url.Values{}
	authQuery.Set("client_id", oauth.CLIClientID)
	authQuery.Set("redirect_uri", "http://127.0.0.1:20000"+oauth.CLICallbackPath)
	authQuery.Set("response_type", "code")
	authQuery.Set("state", "seed-check")
	authQuery.Set("code_challenge", strings.Repeat("a", 43))
	authQuery.Set("code_challenge_method", "S256")

	res := env.get(t, "/api/v1/oauth/authorize?"+authQuery.Encode(), nil)
	r.Equal(http.StatusFound, res.status)
	// Unknown client would render an error, not a login bounce.
	r.Equal("/d/login", func() string {
		loc, err := url.Parse(res.header.Get("Location"))
		r.NoError(err)
		return loc.Path
	}())
}
