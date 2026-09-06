package middleware_test

import (
	"context"
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
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

const mcpTestIssuer = "https://solidping.test"

// setupMCPAuth builds an AuthMiddleware backed by in-memory SQLite plus a real
// auth.Service, and seeds an org + user so a minted access token resolves to a
// real user inside RequireMCPAuth.
func setupMCPAuth(t *testing.T) (*middleware.AuthMiddleware, *auth.Service, *models.User, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: mcpTestIssuer},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}

	authSvc := auth.NewService(dbService, cfg.Auth, cfg, nil, nil)
	mw := middleware.NewAuthMiddleware(authSvc, dbService, cfg)

	org := models.NewOrganization("mcp-auth-org", "")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser("mcp-auth@example.com")
	require.NoError(t, dbService.CreateUser(ctx, user))

	return mw, authSvc, user, ctx
}

func mcpRequest(ctx context.Context, token string) (*http.Request, *httptest.ResponseRecorder) {
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/mcp", http.NoBody)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}

	return r, httptest.NewRecorder()
}

func okMCPHandler(called *bool) httpx.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) error {
		*called = true
		w.WriteHeader(http.StatusOK)

		return nil
	}
}

func TestRequireMCPAuthNoTokenChallenge(t *testing.T) {
	t.Parallel()

	mw, _, _, ctx := setupMCPAuth(t)

	var called bool
	req, rec := mcpRequest(ctx, "")
	err := mw.RequireMCPAuth(okMCPHandler(&called))(rec, req)
	require.NoError(t, err)

	require.False(t, called, "handler must not run without auth")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	challenge := rec.Header().Get("WWW-Authenticate")
	require.True(t, strings.HasPrefix(challenge, "Bearer "), "must be a Bearer challenge")
	require.Contains(t, challenge,
		`resource_metadata="`+mcpTestIssuer+`/.well-known/oauth-protected-resource"`,
		"challenge must point at the protected-resource metadata")
}

func TestRequireMCPAuthValidAudienceBoundToken(t *testing.T) {
	t.Parallel()

	mw, authSvc, user, ctx := setupMCPAuth(t)

	resource := mcpTestIssuer + "/api/v1/mcp"
	token, err := authSvc.GenerateMCPAccessToken(
		ctx, user.UID, "mcp-auth-org", []string{"mcp"}, resource, time.Hour, "",
	)
	require.NoError(t, err)

	var called bool
	req, rec := mcpRequest(ctx, token)
	err = mw.RequireMCPAuth(okMCPHandler(&called))(rec, req)
	require.NoError(t, err)

	require.True(t, called, "audience-bound token must pass")
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireMCPAuthWrongAudienceRejected(t *testing.T) {
	t.Parallel()

	mw, authSvc, user, ctx := setupMCPAuth(t)

	// Token minted for a DIFFERENT resource → must be rejected at the MCP gate.
	token, err := authSvc.GenerateMCPAccessToken(
		ctx, user.UID, "mcp-auth-org", []string{"mcp"},
		"https://other.example.com/api/v1/mcp", time.Hour, "",
	)
	require.NoError(t, err)

	var called bool
	req, rec := mcpRequest(ctx, token)
	err = mw.RequireMCPAuth(okMCPHandler(&called))(rec, req)
	require.NoError(t, err)

	require.False(t, called, "wrong-audience token must not reach the handler")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), "resource_metadata=")
}
