package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// impersonationFixture is a real router (so RoutePattern resolves exactly as
// in production) with RequireAuth on the credential-changing routes, one
// read route, and RequireSuperAdmin on a system route.
type impersonationFixture struct {
	router       *httpx.Router
	authSvc      *auth.Service
	dbSvc        db.Service
	mw           *middleware.AuthMiddleware
	admin        *models.User
	target       *models.User
	adminToken   string
	targetToken  string
	imperToken   string
	lastUser     string
	lastImperson string
	lastAuditImp string
}

// impersonationForbiddenCalls is every credential-changing route, written as
// the concrete request a client would send, with the chi pattern it is
// registered under (mirroring internal/app/server.go).
//
//nolint:gochecknoglobals // test table
var impersonationForbiddenCalls = []struct {
	name, method, pattern, path string
}{
	{"change password", http.MethodPost, "/api/v1/auth/change-password", "/api/v1/auth/change-password"},
	{"2FA setup", http.MethodPost, "/api/v1/auth/2fa/setup", "/api/v1/auth/2fa/setup"},
	{"2FA confirm", http.MethodPost, "/api/v1/auth/2fa/confirm", "/api/v1/auth/2fa/confirm"},
	{"2FA removal", http.MethodDelete, "/api/v1/auth/2fa", "/api/v1/auth/2fa"},
	{
		"passkey add (begin)", http.MethodPost,
		"/api/v1/auth/passkeys/register/begin", "/api/v1/auth/passkeys/register/begin",
	},
	{
		"passkey add (finish)", http.MethodPost,
		"/api/v1/auth/passkeys/register/finish", "/api/v1/auth/passkeys/register/finish",
	},
	{"passkey removal", http.MethodDelete, "/api/v1/auth/passkeys/:uid", "/api/v1/auth/passkeys/abc"},
	{"PAT creation", http.MethodPost, "/api/v1/orgs/:org/tokens", "/api/v1/orgs/acme/tokens"},
	{
		"agent enrollment token", http.MethodPost,
		"/api/v1/orgs/:org/agent-enrollment-tokens", "/api/v1/orgs/acme/agent-enrollment-tokens",
	},
	{"device grant consent", http.MethodPost, "/api/v1/auth/device/consent", "/api/v1/auth/device/consent"},
	{"session revocation", http.MethodDelete, "/api/v1/auth/tokens/:tokenUid", "/api/v1/auth/tokens/abc"},
	{"current session revocation", http.MethodDelete, "/api/v1/auth/tokens/current", "/api/v1/auth/tokens/current"},
	{"switch org (mints a session)", http.MethodPost, "/api/v1/auth/switch-org", "/api/v1/auth/switch-org"},
	{"org creation (mints a session)", http.MethodPost, "/api/v1/orgs", "/api/v1/orgs"},
}

func setupImpersonationFixture(t *testing.T) *impersonationFixture {
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
			JWTSecret:            "test-jwt-secret",
			AccessTokenExpiry:    time.Hour,
			RefreshTokenExpiry:   7 * 24 * time.Hour,
			ImpersonationEnabled: true,
		},
	}

	authSvc := auth.NewService(dbService, cfg.Auth, cfg, nil, nil)
	f := &impersonationFixture{
		router:  httpx.New(),
		authSvc: authSvc,
		dbSvc:   dbService,
		mw:      middleware.NewAuthMiddleware(authSvc, dbService, cfg),
	}

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	f.admin = models.NewUser("root@acme.com")
	f.admin.SuperAdmin = true
	r.NoError(dbService.CreateUser(ctx, f.admin))

	f.target = models.NewUser("alice@acme.com")
	r.NoError(dbService.CreateUser(ctx, f.target))
	r.NoError(dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, f.target.UID, models.MemberRoleAdmin)))

	f.adminToken, err = authSvc.GenerateMCPAccessToken(ctx, f.admin.UID, "acme", nil, "", time.Hour, "")
	r.NoError(err)
	f.targetToken, err = authSvc.GenerateMCPAccessToken(ctx, f.target.UID, "acme", nil, "", time.Hour, "")
	r.NoError(err)

	resp, err := authSvc.Impersonate(ctx, f.admin.UID, f.target.UID, "acme", auth.Context{})
	r.NoError(err)
	f.imperToken = resp.AccessToken

	reached := func(w http.ResponseWriter, req *http.Request) error {
		if user, ok := middleware.GetUserFromContext(req.Context()); ok {
			f.lastUser = user.UID
		}

		f.lastImperson, _ = middleware.GetImpersonatorFromContext(req.Context())
		f.lastAuditImp = audit.ImpersonatorFromContext(req.Context())
		w.WriteHeader(http.StatusOK)

		return nil
	}

	authed := f.router.Use(f.mw.RequireAuth)
	for _, call := range impersonationForbiddenCalls {
		authed.Handle(call.method, call.pattern, reached)
	}

	authed.GET("/api/v1/orgs/:org/checks", reached)
	authed.POST("/api/v1/orgs/:org/checks", reached)
	authed.Use(f.mw.RequireSuperAdmin).GET("/api/v1/system/users", reached)
	f.router.Use(f.mw.RequireMCPAuth).POST("/mcp", reached)

	return f
}

func (f *impersonationFixture) do(t *testing.T, method, path, token string) (int, string) {
	t.Helper()

	f.lastUser, f.lastImperson, f.lastAuditImp = "", "", ""

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		return rec.Code, ""
	}

	return rec.Code, decodeErrorCode(t, rec)
}

// TestImpersonationTokenIsDeniedSuperAdminRoutes: the admin's super-admin
// rights never travel with the impersonation token.
func TestImpersonationTokenIsDeniedSuperAdminRoutes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	f := setupImpersonationFixture(t)

	// Positive control: the actor's own token passes.
	status, _ := f.do(t, http.MethodGet, "/api/v1/system/users", f.adminToken)
	r.Equal(http.StatusOK, status)

	status, _ = f.do(t, http.MethodGet, "/api/v1/system/users", f.imperToken)
	r.Equal(http.StatusForbidden, status)
}

// TestImpersonationTokenCannotChangeCredentials walks the whole
// credential-changing surface. The sub-tests share one fixture, whose handler
// records the last request, so they run sequentially.
//
//nolint:paralleltest,tparallel // shared fixture; see above
func TestImpersonationTokenCannotChangeCredentials(t *testing.T) {
	t.Parallel()
	f := setupImpersonationFixture(t)

	for _, call := range impersonationForbiddenCalls {
		t.Run(call.name, func(t *testing.T) {
			r := require.New(t)

			status, code := f.do(t, call.method, call.path, f.imperToken)
			r.Equal(http.StatusForbidden, status)
			r.Equal(string(base.ErrorCodeImpersonationForbidden), code)

			// Positive control: the target's own session reaches the route.
			status, _ = f.do(t, call.method, call.path, f.targetToken)
			r.Equal(http.StatusOK, status)
		})
	}
}

// TestImpersonationTokenReadsAsTheTarget: an ordinary read and an ordinary
// write run as the target, with the admin on record behind them.
func TestImpersonationTokenReadsAsTheTarget(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	f := setupImpersonationFixture(t)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		status, _ := f.do(t, method, "/api/v1/orgs/acme/checks", f.imperToken)
		r.Equal(http.StatusOK, status, method)
		r.Equal(f.target.UID, f.lastUser, "the user on the context is the target")
		r.Equal(f.admin.UID, f.lastImperson)
		r.Equal(f.admin.UID, f.lastAuditImp, "audit rows will name the admin")
	}

	// Positive control: the target's own token is no impersonation.
	status, _ := f.do(t, http.MethodGet, "/api/v1/orgs/acme/checks", f.targetToken)
	r.Equal(http.StatusOK, status)
	r.Empty(f.lastImperson)
	r.Empty(f.lastAuditImp)
}

// TestImpersonationTokenRefusedOnMCP: MCP is a programmatic surface the route
// denylist cannot see into.
func TestImpersonationTokenRefusedOnMCP(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	f := setupImpersonationFixture(t)

	status, code := f.do(t, http.MethodPost, "/mcp", f.imperToken)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeImpersonationForbidden), code)
}

// TestImpersonationTokenDiesIfTargetBecomesSuperAdmin: promoting the target
// after the token was minted must not lend the token super-admin rights.
func TestImpersonationTokenDiesIfTargetBecomesSuperAdmin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := setupImpersonationFixture(t)

	// RequireAuth re-reads the user row on every request.
	promoted := true
	r.NoError(f.dbSvc.UpdateUser(t.Context(), f.target.UID, &models.UserUpdate{SuperAdmin: &promoted}))

	status, code := f.do(t, http.MethodGet, "/api/v1/orgs/acme/checks", f.imperToken)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeImpersonationForbidden), code)

	status, _ = f.do(t, http.MethodGet, "/api/v1/system/users", f.imperToken)
	r.Equal(http.StatusForbidden, status)
}
