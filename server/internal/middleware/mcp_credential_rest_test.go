package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// MCP credentials on REST (spec 2026-10-16-12): an OAuth access token or a
// scoped PAT works on the MCP endpoint only.

const (
	mcpCredResource  = "https://solidping.test/api/v1/mcp"
	mcpCredDynClient = "dyn-123"
)

type mcpCredFixture struct {
	router *httpx.Router
	tokens map[string]string
}

func setupMCPCredFixture(t *testing.T) *mcpCredFixture {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "https://solidping.test"},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}

	authSvc := auth.NewService(dbService, cfg.Auth, cfg, nil, nil)
	mw := middleware.NewAuthMiddleware(authSvc, dbService, cfg)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	r.NoError(dbService.CreateUser(ctx, user))
	r.NoError(dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	mint := func(scope, clientID string) string {
		tok, mintErr := authSvc.GenerateMCPAccessToken(
			ctx, user.UID, "acme", []string{scope}, mcpCredResource, time.Hour, "", clientID)
		r.NoError(mintErr)

		return tok
	}

	pat := func(scopes []string) string {
		resp, patErr := authSvc.CreatePAT(ctx, "acme", user.UID, auth.CreateTokenRequest{Name: "pat", Scopes: scopes})
		r.NoError(patErr)

		return resp.Token
	}

	session, err := authSvc.GenerateMCPAccessToken(ctx, user.UID, "acme", nil, "", time.Hour, "", "")
	r.NoError(err)

	f := &mcpCredFixture{
		router: httpx.New(),
		tokens: map[string]string{
			"dyn-read":   mint("mcp:read", mcpCredDynClient),
			"dyn-full":   mint("mcp", mcpCredDynClient),
			"cli-read":   mint("mcp:read", auth.CLIOAuthClientID),
			"cli-full":   mint("mcp", auth.CLIOAuthClientID),
			"pat-scoped": pat([]string{"mcp:read"}),
			"pat":        pat(nil),
			"session":    session,
		},
	}

	reached := func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)

		return nil
	}

	authed := f.router.Use(mw.RequireAuth)
	authed.GET("/api/v1/orgs/:org/checks", reached)
	authed.POST("/api/v1/orgs/:org/tokens", reached)
	authed.POST("/api/v1/auth/switch-org", reached)
	authed.DELETE("/api/v1/auth/tokens/current", reached)
	authed.GET("/api/v1/auth/me", reached)
	f.router.Use(mw.RequireMCPAuth).POST("/api/v1/mcp", reached)

	return f
}

func (f *mcpCredFixture) do(t *testing.T, method, path, tokenName string) (int, string) {
	t.Helper()

	token, ok := f.tokens[tokenName]
	require.True(t, ok, tokenName)

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		return rec.Code, ""
	}

	return rec.Code, decodeErrorCode(t, rec)
}

func TestMCPCredentialRefusedOnREST(t *testing.T) {
	t.Parallel()

	f := setupMCPCredFixture(t)

	const (
		checks    = "/api/v1/orgs/acme/checks"
		orgTokens = "/api/v1/orgs/acme/tokens"
		switchOrg = "/api/v1/auth/switch-org"
		current   = "/api/v1/auth/tokens/current"
		me        = "/api/v1/auth/me"
	)

	type mcpCredCase struct {
		name   string
		token  string
		method string
		path   string
		want   int
	}

	controls := []struct{ method, path string }{
		{http.MethodGet, checks},
		{http.MethodPost, orgTokens},
		{http.MethodPost, switchOrg},
		{http.MethodDelete, current},
		{http.MethodGet, me},
	}

	tests := make([]mcpCredCase, 0, 16+2*len(controls))
	tests = append(tests,
		// Regressions: each of these passed before the fix.
		mcpCredCase{"mcp:read dynamic client reads checks", "dyn-read", http.MethodGet, checks, http.StatusUnauthorized},
		mcpCredCase{"mcp dynamic client mints a PAT", "dyn-full", http.MethodPost, orgTokens, http.StatusUnauthorized},
		mcpCredCase{"mcp:read dynamic client switches org", "dyn-read", http.MethodPost, switchOrg, http.StatusUnauthorized},
		mcpCredCase{"mcp:read CLI client mints a PAT", "cli-read", http.MethodPost, orgTokens, http.StatusUnauthorized},
		mcpCredCase{"mcp CLI client reads checks", "cli-full", http.MethodGet, checks, http.StatusUnauthorized},
		mcpCredCase{"scoped PAT reads checks", "pat-scoped", http.MethodGet, checks, http.StatusUnauthorized},
		mcpCredCase{"scoped PAT mints a PAT", "pat-scoped", http.MethodPost, orgTokens, http.StatusUnauthorized},
		mcpCredCase{"scoped PAT switches org", "pat-scoped", http.MethodPost, switchOrg, http.StatusUnauthorized},

		// Allowed.
		mcpCredCase{"mcp:read drops its own grant", "dyn-read", http.MethodDelete, current, http.StatusOK},
		mcpCredCase{"mcp drops its own grant", "dyn-full", http.MethodDelete, current, http.StatusOK},
		mcpCredCase{"mcp:read CLI drops its own grant", "cli-read", http.MethodDelete, current, http.StatusOK},
		mcpCredCase{"mcp:read dynamic client reads /auth/me", "dyn-read", http.MethodGet, me, http.StatusUnauthorized},
		mcpCredCase{"mcp dynamic client reads /auth/me", "dyn-full", http.MethodGet, me, http.StatusUnauthorized},
		mcpCredCase{"mcp:read CLI client reads /auth/me", "cli-read", http.MethodGet, me, http.StatusUnauthorized},
		mcpCredCase{"scoped PAT reads /auth/me", "pat-scoped", http.MethodGet, me, http.StatusUnauthorized},
		mcpCredCase{"legacy loopback CLI mints its PAT", "cli-full", http.MethodPost, orgTokens, http.StatusOK},
		mcpCredCase{"legacy loopback CLI resolves its org", "cli-full", http.MethodGet, me, http.StatusOK},
	)

	// Positive controls: a dashboard session and an unscoped PAT pass
	// everywhere, except switch-org for the PAT: it is bound to its org
	// (spec 2026-10-28-01).
	for _, tok := range []string{"session", "pat"} {
		for _, call := range controls {
			name := tok + " " + call.method + " " + call.path
			want := http.StatusOK

			if tok == "pat" && call.path == switchOrg {
				want = http.StatusForbidden
			}

			tests = append(tests, mcpCredCase{name, tok, call.method, call.path, want})
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, errCode := f.do(t, tt.method, tt.path, tt.token)
			require.Equal(t, tt.want, code)

			if tt.want == http.StatusUnauthorized {
				require.Equal(t, string(base.ErrorCodeInvalidToken), errCode)
			}

			if tt.want == http.StatusForbidden {
				require.Equal(t, string(base.ErrorCodePATForbidden), errCode)
			}
		})
	}
}

// TestMCPCredentialStillWorksOnMCP: the same tokens keep working on the MCP
// endpoint, so the REST refusal is not a blanket rejection.
func TestMCPCredentialStillWorksOnMCP(t *testing.T) {
	t.Parallel()

	f := setupMCPCredFixture(t)

	for _, tok := range []string{"dyn-read", "dyn-full", "cli-read", "cli-full", "pat-scoped", "pat", "session"} {
		code, errCode := f.do(t, http.MethodPost, "/api/v1/mcp", tok)
		require.Equal(t, http.StatusOK, code, "%s: %s", tok, errCode)
	}
}
