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

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// TestMCPConnectBrandNewPasswordUser walks, at the HTTP layer, what a person
// with no SolidPing account goes through when an MCP client sends them to the
// authorize endpoint (fclairamb/solidping-business#35):
//
//	authorize (no session) → login bounce → register with returnTo → confirm
//	(returnTo comes back) → authorize (org-less session) → /no-org bounce →
//	create org → authorize → consent → approve → token → MCP create_check.
//
// Before the fix the register call dropped returnTo, and the org-less
// session was sent to "/d/orgs//oauth/consent".
//
//nolint:maintidx // deliberate single linear walk-through of the whole flow
func TestMCPConnectBrandNewPasswordUser(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newMCPTestEnv(t)
	env.cfg.Auth.RegistrationEmailPattern = ".*"

	const (
		email       = "alice@acme.com"
		redirectURI = "http://127.0.0.1:19321/callback"
		state       = "new-user-state"
		verifier    = "new-user-code-verifier-0123456789abcdefghijklmnop"
	)

	// --- Dynamic client registration, as an MCP client does.
	res := env.postJSON(t, "/api/v1/oauth/register", "", fmt.Sprintf(
		`{"client_name":"acme-mcp","redirect_uris":[%q],`+
			`"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"none"}`,
		redirectURI))
	r.Equal(http.StatusCreated, res.status, "register client: %s", res.body)

	//nolint:tagliatelle // RFC 7591 wire format requires snake_case field names.
	var reg struct {
		ClientID string `json:"client_id"`
	}
	r.NoError(json.Unmarshal(res.body, &reg))

	sum := sha256.Sum256([]byte(verifier))
	authQuery := url.Values{}
	authQuery.Set("client_id", reg.ClientID)
	authQuery.Set("redirect_uri", redirectURI)
	authQuery.Set("response_type", "code")
	authQuery.Set("state", state)
	authQuery.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	authQuery.Set("code_challenge_method", "S256")
	authorizePath := "/api/v1/oauth/authorize?" + authQuery.Encode()

	// --- No session: bounced to the dashboard login with the request as returnTo.
	res = env.get(t, authorizePath, nil)
	r.Equal(http.StatusFound, res.status)
	loginLoc, err := url.Parse(res.header.Get("Location"))
	r.NoError(err)
	r.Equal(config.DashboardBasePath+"/login", loginLoc.Path)
	returnTo := loginLoc.Query().Get("returnTo")
	r.Equal(authorizePath, returnTo)

	// --- Sign up with that returnTo; it rides in the pending registration.
	regBody, err := json.Marshal(map[string]string{
		"email": email, "password": "supersecret123", "returnTo": returnTo,
	})
	r.NoError(err)
	res = env.postJSON(t, "/api/v1/auth/register", "", string(regBody))
	r.Equal(http.StatusOK, res.status, "register: %s", res.body)

	// --- Confirm (the emailed link, in a new tab): returnTo comes back.
	token := env.registrationConfirmToken(t, email)
	res = env.postJSON(t, "/api/v1/auth/confirm-registration", "", fmt.Sprintf(`{"token":%q}`, token))
	r.Equal(http.StatusOK, res.status, "confirm: %s", res.body)

	var confirmed struct {
		AccessToken  string `json:"accessToken"`
		ReturnTo     string `json:"returnTo"`
		Organization *struct {
			Slug string `json:"slug"`
		} `json:"organization"`
	}
	r.NoError(json.Unmarshal(res.body, &confirmed))
	r.NotEmpty(confirmed.AccessToken)
	r.Nil(confirmed.Organization, "precondition: the new account has no org")
	r.Equal(authorizePath, confirmed.ReturnTo)

	// --- Authorize with the org-less session: the org-creation page, not
	// "/orgs//oauth/consent".
	res = env.get(t, confirmed.ReturnTo, map[string]string{"Authorization": "Bearer " + confirmed.AccessToken})
	r.Equal(http.StatusFound, res.status, "authorize (no org): %s", res.body)
	noOrgLoc, err := url.Parse(res.header.Get("Location"))
	r.NoError(err)
	r.Equal(config.DashboardBasePath+"/no-org", noOrgLoc.Path)
	r.Equal(authorizePath, noOrgLoc.Query().Get("returnTo"))

	// --- Approving without an org is refused cleanly.
	res = env.postForm(t, "/api/v1/oauth/authorize", confirmed.AccessToken, authQuery)
	r.Equal(http.StatusForbidden, res.status, "approve (no org): %s", res.body)
	r.Contains(string(res.body), "FORBIDDEN")

	// --- Create the first org: the response carries a session scoped to it.
	res = env.postJSON(t, "/api/v1/orgs", confirmed.AccessToken, `{"name":"Acme"}`)
	r.Equal(http.StatusCreated, res.status, "create org: %s", res.body)

	var org struct {
		Slug        string `json:"slug"`
		AccessToken string `json:"accessToken"`
	}
	r.NoError(json.Unmarshal(res.body, &org))
	r.NotEmpty(org.Slug)
	r.NotEmpty(org.AccessToken)

	// --- Resume authorize: now the consent screen of the new org.
	res = env.get(t, authorizePath, map[string]string{"Authorization": "Bearer " + org.AccessToken})
	r.Equal(http.StatusFound, res.status)
	r.Contains(res.header.Get("Location"), config.DashboardBasePath+"/orgs/"+org.Slug+"/oauth/consent?")

	// --- Approve: the code lands on the client's redirect_uri with the state.
	res = env.postForm(t, "/api/v1/oauth/authorize", org.AccessToken, authQuery)
	r.Equal(http.StatusFound, res.status, "approve: %s", res.body)
	r.True(strings.HasPrefix(res.header.Get("Location"), redirectURI), res.header.Get("Location"))
	cb, err := url.Parse(res.header.Get("Location"))
	r.NoError(err)
	r.Equal(state, cb.Query().Get("state"))
	code := cb.Query().Get("code")
	r.NotEmpty(code)

	// --- Token exchange (PKCE), default `mcp` scope.
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("code", code)
	tokenForm.Set("client_id", reg.ClientID)
	tokenForm.Set("redirect_uri", redirectURI)
	tokenForm.Set("code_verifier", verifier)
	res = env.postForm(t, "/api/v1/oauth/token", "", tokenForm)
	r.Equal(http.StatusOK, res.status, "token: %s", res.body)

	//nolint:tagliatelle // RFC 6749 wire format requires snake_case field names.
	var tok struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	r.NoError(json.Unmarshal(res.body, &tok))
	r.NotEmpty(tok.AccessToken)
	r.Equal("mcp", tok.Scope, "the default scope is the full read-write mcp")

	// --- MCP: the new org's owner can create a check with that token.
	res, _ = env.mcpCall(t, tok.AccessToken, "", mcpInitializeRPC)
	r.Equal(http.StatusOK, res.status)
	sessionID := res.header.Get("Mcp-Session-Id")
	r.NotEmpty(sessionID)

	createRPC := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"create_check",` +
		`"arguments":{"name":"Acme home","slug":"acme-home","type":"http",` +
		`"config":{"url":"https://example.com"}}}}`
	res, rpc := env.mcpCall(t, tok.AccessToken, sessionID, createRPC)
	r.Equal(http.StatusOK, res.status)
	result, ok := rpc["result"].(map[string]any)
	r.True(ok, "create_check result: %v", rpc)
	r.NotEqual(true, result["isError"], "create_check errored: %v", result)

	// Positive control that the check really exists in the new org.
	listRPC := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_checks","arguments":{}}}`
	res, rpc = env.mcpCall(t, tok.AccessToken, sessionID, listRPC)
	r.Equal(http.StatusOK, res.status)
	listed, err := json.Marshal(rpc["result"])
	r.NoError(err)
	r.Contains(string(listed), "acme-home")
}

// postJSON POSTs a JSON body, with bearer when non-empty.
func (e *mcpTestEnv) postJSON(t *testing.T, path, bearer, body string) httpResult {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.ts.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	return e.do(t, req)
}

// postForm POSTs a form body, with bearer when non-empty.
func (e *mcpTestEnv) postForm(t *testing.T, path, bearer string, form url.Values) httpResult {
	t.Helper()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, e.ts.URL+path, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	return e.do(t, req)
}

// registrationConfirmToken reads the confirmation token out of the queued
// registration email for email: the token is only ever stored hashed.
func (e *mcpTestEnv) registrationConfirmToken(t *testing.T, email string) string {
	t.Helper()
	r := require.New(t)

	jobs, err := e.server.dbService.ListJobs(t.Context(), nil, 0)
	r.NoError(err)

	for _, job := range jobs {
		if job.Type != string(jobdef.JobTypeEmail) {
			continue
		}

		if template, _ := job.Config["template"].(string); template != "registration.html" {
			continue
		}

		to, _ := job.Config["to"].([]any)
		if len(to) == 0 || to[0] != email {
			continue
		}

		data, _ := job.Config["templateData"].(map[string]any)
		confirmURL, _ := data["ConfirmURL"].(string)

		if confirmURL != "" {
			return confirmURL[strings.LastIndex(confirmURL, "/")+1:]
		}
	}

	r.Fail("no registration email queued for " + email)

	return ""
}
