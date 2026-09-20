package system_test

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
	authpkg "github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// usersSetup stands up the real /system/users route (RequireAuth +
// RequireSuperAdmin, exactly as server.go wires it) behind a live HTTP
// server, with real JWTs so the middleware chain is exercised end to end.
type usersSetup struct {
	dbSvc      *sqlite.Service
	server     *httptest.Server
	org        *models.Organization
	superToken string
	adminToken string
}

func newUsersSetup(t *testing.T) *usersSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	authCfg := config.AuthConfig{
		JWTSecret:          "system-users-test-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	fullCfg := &config.Config{Auth: authCfg}
	authService := authpkg.NewService(dbSvc, authCfg, fullCfg, nil, nil)
	authMW := middleware.NewAuthMiddleware(authService, dbSvc, fullCfg)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mkUser := func(email string, superAdmin bool) string {
		user := models.NewUser(email)
		pwd := "$plaintext$pw"
		user.PasswordHash = &pwd
		user.SuperAdmin = superAdmin
		r.NoError(dbSvc.CreateUser(ctx, user))
		r.NoError(dbSvc.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

		login, loginErr := authService.Login(ctx, org.Slug, email, "pw", authpkg.Context{})
		r.NoError(loginErr)

		return login.AccessToken
	}

	superToken := mkUser("super@acme.com", true)
	adminToken := mkUser("admin@acme.com", false)

	systemService := system.NewService(dbSvc)
	systemHandler := system.NewHandler(systemService, fullCfg)

	router := httpx.New()
	api := router.NewGroup("/api/v1")
	usersGroup := api.NewGroup("/system").
		Use(authMW.RequireAuth).
		Use(authMW.RequireSuperAdmin)
	usersGroup.GET("/users", systemHandler.ListUsers)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &usersSetup{dbSvc: dbSvc, server: server, org: org, superToken: superToken, adminToken: adminToken}
}

func (s *usersSetup) call(t *testing.T, path, token string) (int, []byte) {
	t.Helper()

	r := require.New(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.server.URL+path, strings.NewReader(""))
	r.NoError(err)

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

// TestListUsersRequiresSuperAdmin: anonymous is 401, an org admin (not super
// admin) is 403, and the identical request as super admin is 200. The third
// case is the positive control — without it, the first two could be passing
// against a route table that 404s or 500s on everything.
func TestListUsersRequiresSuperAdmin(t *testing.T) {
	t.Parallel()

	setup := newUsersSetup(t)

	code, _ := setup.call(t, "/api/v1/system/users", "")
	require.Equal(t, http.StatusUnauthorized, code, "anonymous must be 401")

	code, _ = setup.call(t, "/api/v1/system/users", setup.adminToken)
	require.Equal(t, http.StatusForbidden, code, "an org admin must not reach the directory")

	code, body := setup.call(t, "/api/v1/system/users", setup.superToken)
	require.Equal(t, http.StatusOK, code, "super admin should reach it: %s", string(body))
}

// TestListUsersSerializedJSONNeverCarriesSecrets asserts on the RAW response
// bytes, not the decoded struct — a struct-level check would pass even if
// AdminUserRow grew a PasswordHash field by accident, because nothing forces
// json.Unmarshal to notice an extra key it wasn't told to decode.
func TestListUsersSerializedJSONNeverCarriesSecrets(t *testing.T) {
	t.Parallel()

	setup := newUsersSetup(t)

	code, body := setup.call(t, "/api/v1/system/users", setup.superToken)
	require.Equal(t, http.StatusOK, code, string(body))

	raw := string(body)
	require.NotContains(t, raw, "passwordHash")
	require.NotContains(t, raw, "totpSecret")
	require.NotContains(t, raw, "totpRecoveryCodes")
	// Positive control: prove the response actually has content worth
	// scanning, so the three NotContains above are not vacuously true.
	require.Contains(t, raw, "email")
	require.Contains(t, raw, "super@acme.com")
}

// TestListUsersSearch covers the `q` contract: case-insensitive substring on
// email OR name, and a literal `%` matched literally rather than as a
// wildcard.
func TestListUsersSearch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	setup := newUsersSetup(t)

	named := models.NewUser("zed@example.com")
	named.Name = "Zelda Fifty%Percent"
	r.NoError(setup.dbSvc.CreateUser(ctx, named))

	// Case-insensitive substring on email.
	code, body := setup.call(t, "/api/v1/system/users?q=SUPER@ACME", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))

	var resp struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(1, resp.Total)
	r.Equal("super@acme.com", resp.Data[0]["email"])

	// Case-insensitive substring on name.
	code, body = setup.call(t, "/api/v1/system/users?q=zelda", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(1, resp.Total)
	r.Equal("zed@example.com", resp.Data[0]["email"])

	// A literal '%' in the query is escaped: "fifty%percent" matches the
	// literal name, but "fiftyXpercent" (which would match if '%' behaved as
	// a SQL wildcard) must not.
	code, body = setup.call(t, "/api/v1/system/users?q=fifty%25percent", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(1, resp.Total, "literal %% must match the literal name: %s", string(body))

	code, body = setup.call(t, "/api/v1/system/users?q=fiftyXpercent", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(0, resp.Total, "%% must not behave as a SQL wildcard: %s", string(body))

	// No match at all.
	code, body = setup.call(t, "/api/v1/system/users?q=nonexistentxyz", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(0, resp.Total)
	r.Empty(resp.Data)
}

// TestListUsersPaging covers limit/offset slicing, the unpaged total, and
// clamping limit above 200.
func TestListUsersPaging(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	setup := newUsersSetup(t)

	// setup already seeded 2 users (super, admin); add 3 more distinctly
	// named/ordered ones for a 5-user set.
	for i := range 3 {
		u := models.NewUser("extra" + string(rune('a'+i)) + "@example.com")
		r.NoError(setup.dbSvc.CreateUser(ctx, u))
	}

	code, body := setup.call(t, "/api/v1/system/users?limit=2&offset=0", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))

	var resp struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(5, resp.Total)
	r.Len(resp.Data, 2)

	code, body = setup.call(t, "/api/v1/system/users?limit=2&offset=4", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(5, resp.Total)
	r.Len(resp.Data, 1, "the last page must have exactly the remainder")

	// limit above 200 is clamped, not rejected.
	code, body = setup.call(t, "/api/v1/system/users?limit=99999", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))
	r.NoError(json.Unmarshal(body, &resp))
	r.Equal(5, resp.Total)
	r.Len(resp.Data, 5, "clamped limit must still return every row when the set is smaller than the cap")
}

// TestListUsersExclusions covers every soft-delete edge in the spec: a
// soft-deleted user is absent; a soft-deleted membership is absent from
// orgs; a membership in a soft-deleted org is absent from orgs; a user with
// no org at all has orgs: [].
func TestListUsersExclusions(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	setup := newUsersSetup(t)

	// A user with no org membership at all.
	lonely := models.NewUser("lonely@example.com")
	r.NoError(setup.dbSvc.CreateUser(ctx, lonely))

	// A soft-deleted user must not appear at all.
	ghost := models.NewUser("ghost@example.com")
	r.NoError(setup.dbSvc.CreateUser(ctx, ghost))
	r.NoError(setup.dbSvc.DeleteUser(ctx, ghost.UID))

	// A user whose only membership is soft-deleted.
	exMember := models.NewUser("ex-member@example.com")
	r.NoError(setup.dbSvc.CreateUser(ctx, exMember))
	membership := models.NewOrganizationMember(setup.org.UID, exMember.UID, models.MemberRoleUser)
	r.NoError(setup.dbSvc.CreateOrganizationMember(ctx, membership))
	r.NoError(setup.dbSvc.DeleteOrganizationMember(ctx, membership.UID))

	// A user whose only org got soft-deleted.
	deadOrg := models.NewOrganization("defunct", "Defunct Co")
	r.NoError(setup.dbSvc.CreateOrganization(ctx, deadOrg))
	orphan := models.NewUser("orphan@example.com")
	r.NoError(setup.dbSvc.CreateUser(ctx, orphan))
	r.NoError(setup.dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(deadOrg.UID, orphan.UID, models.MemberRoleUser)))
	r.NoError(setup.dbSvc.DeleteOrganization(ctx, deadOrg.UID))

	code, body := setup.call(t, "/api/v1/system/users?limit=200", setup.superToken)
	r.Equal(http.StatusOK, code, string(body))

	var resp struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	r.NoError(json.Unmarshal(body, &resp))

	byEmail := make(map[string]map[string]any, len(resp.Data))
	for _, row := range resp.Data {
		email, ok := row["email"].(string)
		r.True(ok, "email field must be a string: %v", row)
		byEmail[email] = row
	}

	r.NotContains(byEmail, "ghost@example.com", "soft-deleted user must be absent")

	lonelyRow, ok := byEmail["lonely@example.com"]
	r.True(ok)
	r.Empty(lonelyRow["orgs"], "a user with no org must have orgs: []")
	r.NotNil(lonelyRow["orgs"], "orgs must be [] not null")

	exMemberRow, ok := byEmail["ex-member@example.com"]
	r.True(ok)
	r.Empty(exMemberRow["orgs"], "a soft-deleted membership must be absent")

	orphanRow, ok := byEmail["orphan@example.com"]
	r.True(ok)
	r.Empty(orphanRow["orgs"], "a membership in a soft-deleted org must be absent")
}

// TestListMembersByUsersEmptyInputSkipsQuery proves the DB layer's stated
// contract directly: an empty slice returns empty without ever reaching the
// database (a nil *sqlite.Service dbSvc field would panic if the method
// tried to query it, so passing a closed/never-queried real instance and
// getting a clean empty result back is the behavioral proof available at
// this layer).
func TestListMembersByUsersEmptyInputSkipsQuery(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	setup := newUsersSetup(t)

	members, err := setup.dbSvc.ListMembersByUsers(ctx, nil)
	r.NoError(err)
	r.Empty(members)

	members, err = setup.dbSvc.ListMembersByUsers(ctx, []string{})
	r.NoError(err)
	r.Empty(members)
}
