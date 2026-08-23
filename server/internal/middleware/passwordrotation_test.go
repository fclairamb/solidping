package middleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// rotationFixture is a RequireAuth wired to real SQLite + a real auth.Service,
// so the tests below exercise the actual token → user → gate path rather than a
// hand-placed context value.
type rotationFixture struct {
	mw      *middleware.AuthMiddleware
	authSvc *auth.Service
	dbSvc   db.Service
	ctx     context.Context //nolint:containedctx // test fixture, mirrors setupMCPAuth
}

func setupRotationFixture(t *testing.T) *rotationFixture {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

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

	return &rotationFixture{
		mw:      middleware.NewAuthMiddleware(authSvc, dbService, cfg),
		authSvc: authSvc,
		dbSvc:   dbService,
		ctx:     ctx,
	}
}

// user seeds a user and returns it plus a valid access token for it.
func (f *rotationFixture) user(t *testing.T, email string, flagged bool) (*models.User, string) {
	t.Helper()

	user := models.NewUser(email)
	user.MustChangePassword = flagged
	require.NoError(t, f.dbSvc.CreateUser(f.ctx, user))

	token, err := f.authSvc.GenerateMCPAccessToken(user.UID, "acme", nil, "", time.Hour, "")
	require.NoError(t, err)

	return user, token
}

func (f *rotationFixture) call(t *testing.T, token, method, path string) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	var reached bool

	handler := f.mw.RequireAuth(func(w http.ResponseWriter, _ *http.Request) error {
		reached = true
		w.WriteHeader(http.StatusOK)

		return nil
	})

	req := httptest.NewRequestWithContext(f.ctx, method, path, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	require.NoError(t, handler(rec, req))

	return rec, reached
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Title string `json:"title"`
		Code  string `json:"code"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Title, "the refusal must carry a human message, not just a code")

	return body.Code
}

// TestRequireAuthBlocksFlaggedUser is the core negative: a VALID credential for
// a flagged account reaches nothing. Each path here is a distinct surface the
// spec names — org data, PAT creation (the walk-around-the-gate attempt), and
// the MCP resource server.
func TestRequireAuthBlocksFlaggedUser(t *testing.T) {
	t.Parallel()

	fixture := setupRotationFixture(t)
	_, token := fixture.user(t, "flagged@example.com", true)

	blocked := []struct {
		name   string
		method string
		path   string
	}{
		{"org checks", http.MethodGet, "/api/v1/orgs/acme/checks"},
		{"personal access token creation", http.MethodPost, "/api/v1/orgs/acme/tokens"},
		{"profile update", http.MethodPatch, "/api/v1/auth/me"},
		{"token list", http.MethodGet, "/api/v1/auth/tokens"},
		{"mcp", http.MethodPost, "/api/v1/mcp"},
		{"switch org", http.MethodPost, "/api/v1/auth/switch-org"},
	}

	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			rec, reached := fixture.call(t, token, tc.method, tc.path)

			r.False(reached, "the handler must never run for a flagged account")
			r.Equal(http.StatusForbidden, rec.Code)
			r.Equal("PASSWORD_CHANGE_REQUIRED", decodeErrorCode(t, rec),
				"clients route on this code; a generic FORBIDDEN would strand them")
		})
	}
}

// TestRequireAuthAllowsRotationEndpoints is the counterweight: the allowlist
// must actually work, or a flagged account is bricked rather than blocked.
func TestRequireAuthAllowsRotationEndpoints(t *testing.T) {
	t.Parallel()

	fixture := setupRotationFixture(t)
	_, token := fixture.user(t, "flagged-allowed@example.com", true)

	allowed := []struct {
		name   string
		method string
		path   string
	}{
		{"the rotation itself", http.MethodPost, "/api/v1/auth/change-password"},
		{"discovering the state", http.MethodGet, "/api/v1/auth/me"},
		{"leaving", http.MethodPost, "/api/v1/auth/logout"},
	}

	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			rec, reached := fixture.call(t, token, tc.method, tc.path)

			r.True(reached, "a flagged account must still reach %s", tc.path)
			r.Equal(http.StatusOK, rec.Code)
		})
	}
}

// TestRequireAuthUnflaggedUserUnaffected is the baseline that proves the gate
// is keyed on the flag and nothing else — including the SSO shape, which has no
// password at all and must not be locked out by the column's existence.
func TestRequireAuthUnflaggedUserUnaffected(t *testing.T) {
	t.Parallel()

	fixture := setupRotationFixture(t)

	_, ordinary := fixture.user(t, "ordinary@example.com", false)

	ssoUser := models.NewUser("sso@example.com")
	ssoUser.PasswordHash = nil
	require.NoError(t, fixture.dbSvc.CreateUser(fixture.ctx, ssoUser))
	ssoToken, err := fixture.authSvc.GenerateMCPAccessToken(ssoUser.UID, "acme", nil, "", time.Hour, "")
	require.NoError(t, err)

	for name, token := range map[string]string{"password user": ordinary, "sso user with no password": ssoToken} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			rec, reached := fixture.call(t, token, http.MethodGet, "/api/v1/orgs/acme/checks")
			r.True(reached)
			r.Equal(http.StatusOK, rec.Code)
		})
	}
}

// TestRequireAuthClearingFlagRestoresAccess closes the loop: the same token,
// on the same path, goes from 403 to 200 once the flag is cleared. This is what
// makes the gate a temporary block rather than a permanent revocation.
func TestRequireAuthClearingFlagRestoresAccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := setupRotationFixture(t)
	user, token := fixture.user(t, "clears@example.com", true)

	rec, reached := fixture.call(t, token, http.MethodGet, "/api/v1/orgs/acme/checks")
	r.False(reached)
	r.Equal(http.StatusForbidden, rec.Code)

	cleared := false
	r.NoError(fixture.dbSvc.UpdateUser(fixture.ctx, user.UID,
		&models.UserUpdate{MustChangePassword: &cleared}))

	rec, reached = fixture.call(t, token, http.MethodGet, "/api/v1/orgs/acme/checks")
	r.True(reached, "the pre-existing session must work again once the rotation is done")
	r.Equal(http.StatusOK, rec.Code)
}

// TestRequireMCPAuthBlocksFlaggedUser covers the second auth entry point. It
// has its own token validation (audience binding) and its own failure shape, so
// it needs its own proof that the gate is present — a token minted for the MCP
// resource is exactly the credential a flagged user would try to reuse.
func TestRequireMCPAuthBlocksFlaggedUser(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := setupRotationFixture(t)

	user := models.NewUser("flagged-mcp@example.com")
	user.MustChangePassword = true
	r.NoError(fixture.dbSvc.CreateUser(fixture.ctx, user))

	resource := "https://solidping.test/api/v1/mcp"
	token, err := fixture.authSvc.GenerateMCPAccessToken(
		user.UID, "acme", []string{"mcp"}, resource, time.Hour, "")
	r.NoError(err)

	var reached bool

	handler := fixture.mw.RequireMCPAuth(httpx.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		reached = true
		w.WriteHeader(http.StatusOK)

		return nil
	}))

	req := httptest.NewRequestWithContext(fixture.ctx, http.MethodPost, "/api/v1/mcp", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.NoError(handler(rec, req))

	r.False(reached)
	r.Equal(http.StatusForbidden, rec.Code)
	r.Equal("PASSWORD_CHANGE_REQUIRED", decodeErrorCode(t, rec))
}
