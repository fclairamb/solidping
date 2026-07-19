package middleware_test

import (
	"net/http"
	"net/http/httptest"
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

// TestRequireOrgAccess_RESTRoutesRejectForeignOrg is the REST half of the
// cross-tenant fix (spec 2026-07-15-01): it drives the real
// RequireAuth -> RequireOrgAccess chain over the exact routes the leak exposed
// (/orgs/:org/checks, /members, /incidents) with real minted tokens, and
// asserts a non-super-admin whose token is scoped to another org (or who is a
// non-member) is refused with 403 — before the bug they all returned 200.
func TestRequireOrgAccess_RESTRoutesRejectForeignOrg(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	fullCfg := &config.Config{Auth: authCfg}
	authService := auth.NewService(dbSvc, authCfg, fullCfg, nil, nil)
	authMw := middleware.NewAuthMiddleware(authService, dbSvc, fullCfg)

	orgTest := models.NewOrganization("test", "Test Org")
	orgDefault := models.NewOrganization("default", "Default Org")
	r.NoError(dbSvc.CreateOrganization(ctx, orgTest))
	r.NoError(dbSvc.CreateOrganization(ctx, orgDefault))

	// A non-super-admin who is a member of "test" ONLY.
	user := models.NewUser("member@example.com")
	pwd := "$plaintext$pw"
	user.PasswordHash = &pwd
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(orgTest.UID, user.UID, models.MemberRoleAdmin)))

	loginResp, err := authService.Login(ctx, "test", "member@example.com", "pw", auth.Context{})
	r.NoError(err)
	testToken := loginResp.AccessToken

	ok := func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)

		return nil
	}
	guard := func(h httpx.HandlerFunc) httpx.HandlerFunc {
		return authMw.RequireAuth(authMw.RequireOrgAccess(h))
	}

	router := httpx.New()
	for _, suffix := range []string{"/checks", "/members", "/incidents"} {
		router.GET("/api/v1/orgs/:org"+suffix, guard(ok))
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	get := func(t *testing.T, org, suffix, token string) int {
		t.Helper()
		rr := require.New(t)
		req, err := http.NewRequestWithContext(
			t.Context(), http.MethodGet, server.URL+"/api/v1/orgs/"+org+suffix, http.NoBody)
		rr.NoError(err)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		rr.NoError(err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		return resp.StatusCode
	}

	for _, suffix := range []string{"/checks", "/members", "/incidents"} {
		t.Run("member reaches own org"+suffix, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(http.StatusOK, get(t, "test", suffix, testToken))
		})
		t.Run("member of test is 403 on default"+suffix, func(t *testing.T) {
			t.Parallel()
			// The bug: this returned 200 and leaked default's data (member
			// emails on /members). With the fix the foreign-org token is denied.
			require.New(t).Equal(http.StatusForbidden, get(t, "default", suffix, testToken))
		})
		t.Run("no token is 401"+suffix, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(http.StatusUnauthorized, get(t, "test", suffix, ""))
		})
	}
}
