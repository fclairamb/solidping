package entitlements_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	authpkg "github.com/fclairamb/solidping/server/internal/handlers/auth"
	enthandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// adminSetup stands up the REAL route registration (the same function
// server.go calls) behind a live HTTP server with real JWTs, plus two orgs so
// cross-org targeting can be exercised.
type adminSetup struct {
	dbSvc      *sqlite.Service
	svc        *entcore.Service
	server     *httptest.Server
	orgA       *models.Organization
	orgB       *models.Organization
	superToken string
	adminToken string
}

func newAdminSetup(t *testing.T) *adminSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	authCfg := config.AuthConfig{
		JWTSecret:          "entitlements-admin-test-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	fullCfg := &config.Config{Auth: authCfg}
	authService := authpkg.NewService(dbSvc, authCfg, fullCfg, nil, nil)
	authMW := middleware.NewAuthMiddleware(authService, dbSvc, fullCfg)

	orgA := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("acmetech", "Acme Tech")
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	mkUser := func(email string, superAdmin bool) string {
		user := models.NewUser(email)
		pwd := "$plaintext$pw"
		user.PasswordHash = &pwd
		user.SuperAdmin = superAdmin
		r.NoError(dbSvc.CreateUser(ctx, user))
		// Deliberately an org ADMIN of org A: the point of the negative case is
		// that the highest ORG role is still not enough for an instance-level
		// editor, and the point of the positive case is that a superadmin
		// signed into org A can edit org B.
		r.NoError(dbSvc.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(orgA.UID, user.UID, models.MemberRoleAdmin)))

		login, loginErr := authService.Login(ctx, orgA.Slug, email, "pw", authpkg.Context{})
		r.NoError(loginErr)

		return login.AccessToken
	}

	superToken := mkUser("super@acme.com", true)
	adminToken := mkUser("admin@acme.com", false)

	svc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSaaS), 0)

	router := httpx.New()
	api := router.NewGroup("/api/v1")
	enthandler.RegisterAdminRoutes(api, authMW, enthandler.NewHandler(svc, dbSvc, fullCfg))

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &adminSetup{
		dbSvc: dbSvc, svc: svc, server: server,
		orgA: orgA, orgB: orgB,
		superToken: superToken, adminToken: adminToken,
	}
}

func (s *adminSetup) call(
	t *testing.T, method, path, token, body string,
) (int, []byte) {
	t.Helper()

	r := require.New(t)

	req, err := http.NewRequestWithContext(
		t.Context(), method, s.server.URL+path, strings.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	r.NoError(err)

	return resp.StatusCode, raw
}

// adminOperation is one endpoint of the superadmin editor.
type adminOperation struct {
	name   string
	method string
	path   string
	body   string
	// wantSuperAdmin is the REAL success code, so the 401/403 assertions below
	// are not passing on a route table that simply 404s.
	wantSuperAdmin int
}

// TestAdminEntitlementsRoutesRequireSuperAdmin drives the real registration
// through a live server: nobody below super admin reaches any endpoint, and a
// super admin reaches all of them.
func TestAdminEntitlementsRoutesRequireSuperAdmin(t *testing.T) {
	t.Parallel()

	setup := newAdminSetup(t)

	base := "/api/v1/system/entitlements"
	operations := []adminOperation{
		{"list", http.MethodGet, base, "", http.StatusOK},
		{"get", http.MethodGet, base + "/acme", "", http.StatusOK},
		{"put", http.MethodPut, base + "/acme", `{"limits":{"maxChecks":500}}`, http.StatusOK},
		{"release", http.MethodDelete, base + "/acme", "", http.StatusOK},
	}

	for _, op := range operations {
		code, _ := setup.call(t, op.method, op.path, "", op.body)
		require.Equal(t, http.StatusUnauthorized, code,
			"unauthenticated must be 401 on %s", op.name)

		code, _ = setup.call(t, op.method, op.path, setup.adminToken, op.body)
		require.Equal(t, http.StatusForbidden, code,
			"an org admin must not reach the instance entitlements editor: %s", op.name)
	}

	// POSITIVE CONTROL.
	for _, op := range operations {
		code, body := setup.call(t, op.method, op.path, setup.superToken, op.body)
		require.Equal(t, op.wantSuperAdmin, code,
			"super admin should reach %s: %s", op.name, string(body))
	}
}

// TestAdminCanEditAnotherOrg proves the cross-org half of the authz contract:
// the superadmin is a member of org A only, and edits org B.
func TestAdminCanEditAnotherOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	setup := newAdminSetup(t)

	code, body := setup.call(t, http.MethodPut, "/api/v1/system/entitlements/"+setup.orgB.Slug,
		setup.superToken, `{"limits":{"maxChecks":4242,"maxChecksPerMinute":900}}`)
	r.Equal(http.StatusOK, code, string(body))

	resolved, err := setup.svc.Resolve(t.Context(), setup.orgB.UID)
	r.NoError(err)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(4242, *resolved.Limits.MaxChecks)
	r.Equal(models.EntitlementSourceAdmin, resolved.Source)

	// And org A, which the superadmin actually belongs to, is untouched.
	other, err := setup.svc.Resolve(t.Context(), setup.orgA.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceDefault, other.Source)
}

// TestAdminPutIgnoresClaimedSource makes sure a superadmin write is always an
// ADMIN write: a body claiming source=billing-service must not be able to
// launder itself past the precedence rule.
func TestAdminPutIgnoresClaimedSource(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	setup := newAdminSetup(t)

	code, body := setup.call(t, http.MethodPut, "/api/v1/system/entitlements/acme",
		setup.superToken, `{"source":"billing-service","limits":{"maxChecks":7}}`)
	r.Equal(http.StatusOK, code, string(body))

	resolved, err := setup.svc.Resolve(t.Context(), setup.orgA.UID)
	r.NoError(err)
	r.Equal(models.EntitlementSourceAdmin, resolved.Source)
}

// TestAdminGetReturnsStoredDefaultsAndAudits pins the detail payload the
// editor needs: the resolved values, whether a row is actually stored, the
// deployment defaults a release returns to, and the audit trail.
func TestAdminGetReturnsStoredDefaultsAndAudits(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	setup := newAdminSetup(t)

	// Before any write: no stored row at all, and the resolved source is
	// "default" — the state the editor must render as "Free defaults".
	code, body := setup.call(t, http.MethodGet, "/api/v1/system/entitlements/acme",
		setup.superToken, "")
	r.Equal(http.StatusOK, code, string(body))

	var before struct {
		Source   models.EntitlementSource `json:"source"`
		Stored   *json.RawMessage         `json:"stored"`
		Defaults entcore.Limits           `json:"defaults"`
		Audits   []json.RawMessage        `json:"audits"`
	}

	r.NoError(json.Unmarshal(body, &before))
	r.Nil(before.Stored, "an org with no row must report no stored row")
	r.Equal(models.EntitlementSourceDefault, before.Source)
	r.NotNil(before.Defaults.MaxChecks, "SaaS defaults cap checks")
	r.Empty(before.Audits)

	code, body = setup.call(t, http.MethodPut, "/api/v1/system/entitlements/acme",
		setup.superToken, `{"limits":{"maxChecks":900}}`)
	r.Equal(http.StatusOK, code, string(body))

	code, body = setup.call(t, http.MethodGet, "/api/v1/system/entitlements/acme",
		setup.superToken, "")
	r.Equal(http.StatusOK, code, string(body))

	var after struct {
		Source models.EntitlementSource `json:"source"`
		Stored *struct {
			Source models.EntitlementSource `json:"source"`
			Limits entcore.Limits           `json:"limits"`
		} `json:"stored"`
		Audits []struct {
			Source string `json:"Source"`
		} `json:"audits"`
	}

	r.NoError(json.Unmarshal(body, &after))
	r.Equal(models.EntitlementSourceAdmin, after.Source)
	r.NotNil(after.Stored)
	r.Equal(models.EntitlementSourceAdmin, after.Stored.Source)
	r.NotNil(after.Stored.Limits.MaxChecks)
	r.Equal(900, *after.Stored.Limits.MaxChecks)
	r.Len(after.Audits, 1)
}

// TestAdminListFlagsOverLimitOrgs covers the resolved open question: the org
// list surfaces orgs whose scheduled demand exceeds their resolved cap.
func TestAdminListFlagsOverLimitOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	setup := newAdminSetup(t)
	ctx := t.Context()

	// Two checks a minute each, against a cap of one.
	for _, slug := range []string{"one", "two"} {
		check := models.NewCheck(setup.orgB.UID, slug, "http")
		check.Enabled = true
		check.Period = timeutils.Duration(30 * time.Second)
		r.NoError(setup.dbSvc.CreateCheck(ctx, check))
	}

	r.NoError(setup.svc.Set(ctx, setup.orgB.UID, entcore.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entcore.Limits{MaxChecksPerMinute: entcore.Int(1)},
	}, "test", ""))

	code, body := setup.call(t, http.MethodGet, "/api/v1/system/entitlements?q=acmetech",
		setup.superToken, "")
	r.Equal(http.StatusOK, code, string(body))

	var listed struct {
		Data []struct {
			Slug            string `json:"slug"`
			OverCheckRate   bool   `json:"overCheckRate"`
			ChecksPerMinute *struct {
				Demand float64 `json:"demand"`
				Limit  *int    `json:"limit"`
			} `json:"checksPerMinute"`
			AdminOverrideSince *time.Time `json:"adminOverrideSince"`
		} `json:"data"`
		Total int `json:"total"`
	}

	r.NoError(json.Unmarshal(body, &listed))
	r.Equal(1, listed.Total, "the q filter must narrow the list")
	r.Len(listed.Data, 1)
	r.Equal("acmetech", listed.Data[0].Slug)
	r.True(listed.Data[0].OverCheckRate, "4/min against a cap of 1 is over")
	r.NotNil(listed.Data[0].ChecksPerMinute)
	r.NotNil(listed.Data[0].AdminOverrideSince, "an admin row reports when it was set")

	// An org inside its (unlimited-by-absence) cap is not flagged. Decoded into
	// a FRESH value: unmarshalling into the slice above would reuse its
	// elements and leave stale fields standing where the new JSON omits them.
	var both struct {
		Data []struct {
			Slug               string     `json:"slug"`
			OverCheckRate      bool       `json:"overCheckRate"`
			AdminOverrideSince *time.Time `json:"adminOverrideSince"`
		} `json:"data"`
		Total int `json:"total"`
	}

	code, body = setup.call(t, http.MethodGet, "/api/v1/system/entitlements?q=acme&limit=10",
		setup.superToken, "")
	r.Equal(http.StatusOK, code, string(body))
	r.NoError(json.Unmarshal(body, &both))
	r.Equal(2, both.Total, "a substring search matches both orgs")

	seen := false

	for _, row := range both.Data {
		if row.Slug == "acme" {
			seen = true

			r.False(row.OverCheckRate, "an org with no checks is never over its cap")
			r.Nil(row.AdminOverrideSince, "an org with no stored row has no override date")
		}
	}

	r.True(seen, "the unflagged org must actually be in the page")
}

// TestAdminGetUnknownOrgIs404 keeps a typo'd slug from reading as "an org with
// default limits".
func TestAdminGetUnknownOrgIs404(t *testing.T) {
	t.Parallel()

	setup := newAdminSetup(t)

	code, _ := setup.call(t, http.MethodGet, "/api/v1/system/entitlements/nope",
		setup.superToken, "")
	require.Equal(t, http.StatusNotFound, code)
}
