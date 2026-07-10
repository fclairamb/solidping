package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// This file covers the HTTP-routing/method layer of the MCP endpoint against
// the REAL router (SetupRoutes), spec 2026-07-10-06:
//   - GET /api/v1/mcp: 302 to the dashboard MCP page for browsers, 405 for
//     MCP clients probing with Accept: text/event-stream;
//   - DELETE /api/v1/mcp: authenticated session termination;
//   - unmatched /api/v1/* paths answer JSON NOT_FOUND, never the SPA shell;
//   - a full client-shaped walk-through of the documented connect flow:
//     OAuth discovery -> dynamic client registration -> authorize (consent)
//     -> PKCE token exchange -> initialize -> tools/list -> tools/call,
//     plus the PAT path (mint a PAT, Bearer straight to POST).

const (
	mcpE2EOrg      = "mcp-e2e"
	mcpE2EEmail    = "mcp-e2e@example.com"
	mcpE2EPassword = "mcp-e2e-password-1234"
)

// mcpTestEnv is a fully wired server (real NewServer + real SetupRoutes over
// in-memory SQLite) listening on an httptest socket, with one seeded org and
// admin member.
type mcpTestEnv struct {
	ts     *httptest.Server
	client *http.Client
}

func newMCPTestEnv(t *testing.T) *mcpTestEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = "sqlite-memory"
	cfg.Auth.JWTSecret = "mcp-endpoint-test-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	// OAuth metadata, audience binding, and redirects derive from
	// cfg.Server.BaseURL at request time, so rebase it onto the live socket —
	// the walk-through below then follows discovery URLs like a real client.
	cfg.Server.BaseURL = ts.URL

	// Seed an org and an admin member with a password login.
	org := models.NewOrganization(mcpE2EOrg, "")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	hash, err := passwords.Hash(mcpE2EPassword)
	r.NoError(err)
	now := time.Now()
	user := models.NewUser(mcpE2EEmail)
	user.PasswordHash = &hash
	user.EmailVerifiedAt = &now
	r.NoError(server.dbService.CreateUser(ctx, user))
	r.NoError(server.dbService.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	client := &http.Client{
		// Redirects are assertions in these tests — never follow them.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &mcpTestEnv{ts: ts, client: client}
}

// do sends a request and returns the response with its fully-read body.
func (e *mcpTestEnv) do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp, body
}

func (e *mcpTestEnv) get(t *testing.T, path string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.ts.URL+path, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return e.do(t, req)
}

// login walks the documented password login and returns the session JWT.
func (e *mcpTestEnv) login(t *testing.T) string {
	t.Helper()
	r := require.New(t)

	body := fmt.Sprintf(`{"org":%q,"email":%q,"password":%q}`, mcpE2EOrg, mcpE2EEmail, mcpE2EPassword)
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, e.ts.URL+"/api/v1/auth/login", strings.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, respBody := e.do(t, req)
	r.Equal(http.StatusOK, resp.StatusCode, "login failed: %s", respBody)

	var loginResp struct {
		AccessToken string `json:"accessToken"`
	}
	r.NoError(json.Unmarshal(respBody, &loginResp))
	r.NotEmpty(loginResp.AccessToken)

	return loginResp.AccessToken
}

// mcpCall POSTs one JSON-RPC request to /api/v1/mcp and returns the HTTP
// response and decoded body.
func (e *mcpTestEnv) mcpCall(
	t *testing.T, bearer, sessionID, rpcBody string,
) (*http.Response, map[string]any) {
	t.Helper()
	r := require.New(t)

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, e.ts.URL+"/api/v1/mcp", strings.NewReader(rpcBody))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, body := e.do(t, req)

	decoded := map[string]any{}
	if len(bytes.TrimSpace(body)) > 0 {
		r.NoError(json.Unmarshal(body, &decoded), "body: %s", body)
	}

	return resp, decoded
}

func TestMCPEndpointGetAndAPIFallthrough(t *testing.T) {
	t.Parallel()
	env := newMCPTestEnv(t)

	t.Run("browser GET redirects to the dashboard MCP page", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp, _ := env.get(t, "/api/v1/mcp", map[string]string{
			"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		})
		r.Equal(http.StatusFound, resp.StatusCode)
		r.Equal("/dash0/mcp?from=get", resp.Header.Get("Location"))
	})

	t.Run("SSE-probing GET gets the spec 405 with Allow", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp, body := env.get(t, "/api/v1/mcp", map[string]string{
			"Accept": "text/event-stream",
		})
		r.Equal(http.StatusMethodNotAllowed, resp.StatusCode)
		r.Contains(resp.Header.Get("Allow"), "POST")
		r.Contains(resp.Header.Get("Content-Type"), "application/json")

		var errResp base.ErrorResponse
		r.NoError(json.Unmarshal(body, &errResp))
		r.Equal(string(base.ErrorCodeMethodNotAllowed), errResp.Code)
	})

	t.Run("unmatched API path returns JSON NOT_FOUND, not the SPA", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp, body := env.get(t, "/api/v1/definitely-not-a-route", map[string]string{
			"Accept": "text/html,application/xhtml+xml",
		})
		r.Equal(http.StatusNotFound, resp.StatusCode)
		r.Contains(resp.Header.Get("Content-Type"), "application/json")
		r.NotContains(strings.ToLower(string(body)), "<!doctype html")

		var errResp base.ErrorResponse
		r.NoError(json.Unmarshal(body, &errResp))
		r.Equal(string(base.ErrorCodeNotFound), errResp.Code)
	})

	t.Run("non-API unmatched path still serves the SPA", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp, _ := env.get(t, "/some/marketing/page", map[string]string{"Accept": "text/html"})
		// The SPA catch-all answers 200 HTML (embedded app shell); the exact
		// body depends on the embedded build, only the contrast with the API
		// namespace matters here.
		r.Equal(http.StatusOK, resp.StatusCode)
		r.NotContains(resp.Header.Get("Content-Type"), "application/json")
	})

	t.Run("unauthenticated POST keeps the OAuth discovery challenge", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		req, err := http.NewRequestWithContext(
			t.Context(), http.MethodPost, env.ts.URL+"/api/v1/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		r.NoError(err)
		req.Header.Set("Content-Type", "application/json")

		resp, _ := env.do(t, req)
		r.Equal(http.StatusUnauthorized, resp.StatusCode)
		r.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata=`)
	})
}

// TestMCPClientWalkthroughOAuth exercises the documented client connect flow
// end to end over real HTTP: discovery -> DCR -> authorize/consent -> PKCE
// token exchange -> initialize -> tools/list -> tools/call -> DELETE session.
func TestMCPClientWalkthroughOAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newMCPTestEnv(t)

	// --- Discovery: protected-resource metadata names the authorization server.
	resp, body := env.get(t, "/.well-known/oauth-protected-resource", nil)
	r.Equal(http.StatusOK, resp.StatusCode)

	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	r.NoError(json.Unmarshal(body, &prm))
	r.Equal(env.ts.URL+"/api/v1/mcp", prm.Resource)
	r.Len(prm.AuthorizationServers, 1)

	// --- Discovery: authorization-server metadata names every flow endpoint.
	resp, body = env.get(t, "/.well-known/oauth-authorization-server", nil)
	r.Equal(http.StatusOK, resp.StatusCode)

	var asm struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		RegistrationEndpoint  string `json:"registration_endpoint"`
	}
	r.NoError(json.Unmarshal(body, &asm))
	r.NotEmpty(asm.AuthorizationEndpoint)
	r.NotEmpty(asm.TokenEndpoint)
	r.NotEmpty(asm.RegistrationEndpoint)

	// --- Dynamic client registration (public client, loopback redirect).
	redirectURI := "http://127.0.0.1:19999/callback"
	regBody := fmt.Sprintf(
		`{"client_name":"walkthrough","redirect_uris":[%q],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"none"}`,
		redirectURI)
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, asm.RegistrationEndpoint, strings.NewReader(regBody))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	resp, body = env.do(t, req)
	r.Equal(http.StatusCreated, resp.StatusCode, "register: %s", body)

	var reg struct {
		ClientID string `json:"client_id"`
	}
	r.NoError(json.Unmarshal(body, &reg))
	r.NotEmpty(reg.ClientID)

	// --- Authorize: a logged-in session gets bounced to the consent screen.
	sessionToken := env.login(t)
	verifier := "walkthrough-code-verifier-0123456789abcdefghij"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	authQuery := url.Values{}
	authQuery.Set("client_id", reg.ClientID)
	authQuery.Set("redirect_uri", redirectURI)
	authQuery.Set("response_type", "code")
	authQuery.Set("scope", "mcp")
	authQuery.Set("state", "walkthrough-state")
	authQuery.Set("code_challenge", challenge)
	authQuery.Set("code_challenge_method", "S256")

	req, err = http.NewRequestWithContext(
		t.Context(), http.MethodGet, asm.AuthorizationEndpoint+"?"+authQuery.Encode(), nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, _ = env.do(t, req)
	r.Equal(http.StatusFound, resp.StatusCode)
	r.Contains(resp.Header.Get("Location"), "/oauth/consent?")

	// --- The user approves the consent screen -> code lands on redirect_uri.
	req, err = http.NewRequestWithContext(
		t.Context(), http.MethodPost, asm.AuthorizationEndpoint,
		strings.NewReader(authQuery.Encode()))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, body = env.do(t, req)
	r.Equal(http.StatusFound, resp.StatusCode, "approve: %s", body)

	loc, err := url.Parse(resp.Header.Get("Location"))
	r.NoError(err)
	r.True(strings.HasPrefix(resp.Header.Get("Location"), "http://127.0.0.1:19999/callback"))
	code := loc.Query().Get("code")
	r.NotEmpty(code)
	r.Equal("walkthrough-state", loc.Query().Get("state"))

	// --- PKCE token exchange.
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("code", code)
	tokenForm.Set("client_id", reg.ClientID)
	tokenForm.Set("redirect_uri", redirectURI)
	tokenForm.Set("code_verifier", verifier)

	req, err = http.NewRequestWithContext(
		t.Context(), http.MethodPost, asm.TokenEndpoint, strings.NewReader(tokenForm.Encode()))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, body = env.do(t, req)
	r.Equal(http.StatusOK, resp.StatusCode, "token: %s", body)

	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	r.NoError(json.Unmarshal(body, &tok))
	r.NotEmpty(tok.AccessToken)

	// --- MCP: initialize opens a session.
	resp, rpc := env.mcpCall(t, tok.AccessToken, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"walkthrough","version":"1.0"}}}`)
	r.Equal(http.StatusOK, resp.StatusCode)
	sessionID := resp.Header.Get("Mcp-Session-Id")
	r.NotEmpty(sessionID)
	result, ok := rpc["result"].(map[string]any)
	r.True(ok, "initialize result: %v", rpc)
	r.NotEmpty(result["protocolVersion"])

	// --- tools/list names the catalog.
	resp, rpc = env.mcpCall(t, tok.AccessToken, sessionID,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	r.Equal(http.StatusOK, resp.StatusCode)
	result, ok = rpc["result"].(map[string]any)
	r.True(ok, "tools/list result: %v", rpc)
	tools, ok := result["tools"].([]any)
	r.True(ok)
	r.NotEmpty(tools)

	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolMap, isMap := tool.(map[string]any)
		r.True(isMap)
		name, isString := toolMap["name"].(string)
		r.True(isString)
		toolNames = append(toolNames, name)
	}
	r.Contains(toolNames, "list_checks")

	// --- one tools/call round-trip (read tool on the empty seeded org).
	resp, rpc = env.mcpCall(t, tok.AccessToken, sessionID,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_checks","arguments":{}}}`)
	r.Equal(http.StatusOK, resp.StatusCode)
	result, ok = rpc["result"].(map[string]any)
	r.True(ok, "tools/call result: %v", rpc)
	r.NotEqual(true, result["isError"], "tools/call errored: %v", result)

	// --- explicit session termination.
	req, err = http.NewRequestWithContext(
		t.Context(), http.MethodDelete, env.ts.URL+"/api/v1/mcp", nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, _ = env.do(t, req)
	r.Equal(http.StatusNoContent, resp.StatusCode)

	// A second DELETE proves the session really terminated.
	req, err = http.NewRequestWithContext(
		t.Context(), http.MethodDelete, env.ts.URL+"/api/v1/mcp", nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, _ = env.do(t, req)
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

// TestMCPClientWalkthroughPAT covers the documented alternative: mint a PAT
// with the mcp scope and go straight to POST with a bearer header.
func TestMCPClientWalkthroughPAT(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newMCPTestEnv(t)

	sessionToken := env.login(t)

	// Mint a PAT with the mcp scope through the documented endpoint.
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, env.ts.URL+"/api/v1/orgs/"+mcpE2EOrg+"/tokens",
		strings.NewReader(`{"name":"mcp-walkthrough","scopes":["mcp"]}`))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, body := env.do(t, req)
	r.Equal(http.StatusCreated, resp.StatusCode, "create PAT: %s", body)

	var pat struct {
		Token string `json:"token"`
	}
	r.NoError(json.Unmarshal(body, &pat))
	r.NotEmpty(pat.Token)

	// PAT straight to POST: initialize then tools/list.
	resp, rpc := env.mcpCall(t, pat.Token, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"pat-walkthrough","version":"1.0"}}}`)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotEmpty(resp.Header.Get("Mcp-Session-Id"))
	_, ok := rpc["result"].(map[string]any)
	r.True(ok, "initialize result: %v", rpc)

	resp, rpc = env.mcpCall(t, pat.Token, resp.Header.Get("Mcp-Session-Id"),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	r.Equal(http.StatusOK, resp.StatusCode)
	result, ok := rpc["result"].(map[string]any)
	r.True(ok, "tools/list result: %v", rpc)
	r.NotEmpty(result["tools"])
}
