package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// This file is deliberately an INTERNAL test (package middleware, not
// middleware_test): the service-authorization marker is an unexported context
// key, and the "a trusted service still passes" row of the matrix is the one
// that would silently break the billing push if it regressed. Reaching it
// through the real ServiceTokenBypass would mean seeding system parameters and
// signing keys to assert something the key itself already expresses.

// orgWriteEnv holds one org and one user per role.
type orgWriteEnv struct {
	authMw *AuthMiddleware
	org    *models.Organization
	users  map[string]*models.User
}

func newOrgWriteEnv(t *testing.T) *orgWriteEnv {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("writeorg", "Write Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	env := &orgWriteEnv{
		authMw: NewAuthMiddleware(nil, dbSvc, &config.Config{}),
		org:    org,
		users:  map[string]*models.User{},
	}

	mkUser := func(email string, super bool) *models.User {
		user := models.NewUser(email)
		user.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(ctx, user))

		return user
	}

	for _, role := range []models.MemberRole{
		models.MemberRoleOwner, models.MemberRoleAdmin,
		models.MemberRoleUser, models.MemberRoleViewer,
	} {
		user := mkUser(string(role)+"@writeorg.example", false)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, role)))
		env.users[string(role)] = user
	}

	env.users["super"] = mkUser("super@writeorg.example", true)
	env.users["stranger"] = mkUser("stranger@writeorg.example", false)

	return env
}

// guarded wraps a handler that records that it ran.
func (e *orgWriteEnv) guarded() httpxHandler {
	return e.authMw.RequireOrgWrite(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusOK)

		return nil
	})
}

// httpxHandler mirrors httpx.HandlerFunc without importing it for one alias.
type httpxHandler = func(http.ResponseWriter, *http.Request) error

// requestAs builds a request carrying whatever RequireAuth + RequireOrgAccess
// would have put in the context.
func requestAs(user *models.User, org *models.Organization, method string, service bool) *http.Request {
	ctx := context.Background()

	if user != nil {
		ctx = context.WithValue(ctx, base.ContextKeyUser, user)
	}

	if org != nil {
		ctx = context.WithValue(ctx, base.ContextKeyOrganization, org)
	}

	if service {
		ctx = context.WithValue(ctx, serviceAuthContextKey{}, true)
	}

	return httptest.NewRequestWithContext(ctx, method, "/api/v1/orgs/writeorg/checks", http.NoBody)
}

// denialBody decodes the standard error document.
func denialBody(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()

	var body struct {
		Code  string `json:"code"`
		Title string `json:"title"`
	}

	_ = json.NewDecoder(rec.Body).Decode(&body)

	return body.Code, body.Title
}

// TestRequireOrgWrite_AuthMatrix is the unit-level half of the §4 proof:
// every (role × method) cell of the gate, stated once.
func TestRequireOrgWrite_AuthMatrix(t *testing.T) {
	t.Parallel()

	env := newOrgWriteEnv(t)
	guarded := env.guarded()

	writeMethods := []string{
		http.MethodPost, http.MethodPatch, http.MethodPut,
		http.MethodDelete,
	}
	safeMethods := []string{http.MethodGet, http.MethodHead, http.MethodOptions}

	tests := []struct {
		name       string
		userKey    string
		methods    []string
		wantStatus int
	}{
		{"viewer may read", "viewer", safeMethods, http.StatusOK},
		{"viewer may not write", "viewer", writeMethods, http.StatusForbidden},
		{"user may write", "user", writeMethods, http.StatusOK},
		{"admin may write", "admin", writeMethods, http.StatusOK},
		{"owner may write", "owner", writeMethods, http.StatusOK},
		{"super admin may write", "super", writeMethods, http.StatusOK},
		// A non-member never gets this far in production (RequireOrgAccess
		// denies first), but the gate must not be the thing that lets them in.
		{"non-member may not write", "stranger", writeMethods, http.StatusForbidden},
		{"non-member may read", "stranger", safeMethods, http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, method := range tc.methods {
				rec := httptest.NewRecorder()
				_ = guarded(rec, requestAs(env.users[tc.userKey], env.org, method, false))
				require.Equalf(t, tc.wantStatus, rec.Code, "%s %s", tc.userKey, method)
			}
		})
	}
}

// TestRequireOrgWriteDeniesWithTheStandardShape pins the response contract the
// dashboard depends on: 403 + FORBIDDEN (no new error code, so dash0 renders
// Permission Denied and never redirects), carrying the one message the
// route-table proof asserts on.
func TestRequireOrgWriteDeniesWithTheStandardShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newOrgWriteEnv(t)

	rec := httptest.NewRecorder()
	_ = env.guarded()(rec, requestAs(env.users["viewer"], env.org, http.MethodPost, false))

	r.Equal(http.StatusForbidden, rec.Code)

	code, title := denialBody(t, rec)
	r.Equal(string(base.ErrorCodeForbidden), code)
	r.Equal(ViewerWriteMessage, title)
}

// TestRequireOrgWriteLetsTrustedServicesThrough is the row that would break the
// billing entitlements push if it regressed: a service-signed request carries
// no user and no membership, so the role gate must never see it.
func TestRequireOrgWriteLetsTrustedServicesThrough(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newOrgWriteEnv(t)

	rec := httptest.NewRecorder()
	// No user, no org in context — exactly what RequireOrgAccess leaves behind
	// when it short-circuits for a trusted service.
	_ = env.guarded()(rec, requestAs(nil, nil, http.MethodPut, true))

	r.Equal(http.StatusOK, rec.Code)

	// And without the service marker, the same request is denied — otherwise
	// the row above would pass for the wrong reason.
	rec = httptest.NewRecorder()
	_ = env.guarded()(rec, requestAs(nil, nil, http.MethodPut, false))
	r.NotEqual(http.StatusOK, rec.Code)
}

// TestRequireOrgWriteDeniesWithoutContext covers the two ways the gate can be
// mis-wired: placed before RequireAuth (no user) or before RequireOrgAccess
// (no organization). Both must fail closed rather than pass the request on.
func TestRequireOrgWriteDeniesWithoutContext(t *testing.T) {
	t.Parallel()

	env := newOrgWriteEnv(t)

	tests := []struct {
		name       string
		user       *models.User
		org        *models.Organization
		wantStatus int
	}{
		{"no user", nil, env.org, http.StatusUnauthorized},
		{"no organization", env.users["owner"], nil, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			_ = env.guarded()(rec, requestAs(tc.user, tc.org, http.MethodPost, false))
			require.Equal(t, tc.wantStatus, rec.Code)
		})
	}
}

// TestRequireOrgWriteMethodCheckIsCaseExact pins the safe-method switch. chi
// never routes a lowercase method, but a gate that lowercased its input would
// hand a viewer a trivially crafted bypass, so the exactness is asserted rather
// than assumed.
func TestRequireOrgWriteMethodCheckIsCaseExact(t *testing.T) {
	t.Parallel()

	env := newOrgWriteEnv(t)

	for _, method := range []string{"get", "Get", "hEaD", "oPtIoNs", "TRACE"} {
		rec := httptest.NewRecorder()
		_ = env.guarded()(rec, requestAs(env.users["viewer"], env.org, method, false))
		require.Equalf(t, http.StatusForbidden, rec.Code,
			"%q must not be treated as a safe method", method)
	}
}

// TestRequireOrgWriteReadsTheMembershipRow is the "membership row, not claims"
// decision made observable: nothing about the caller changes except the row, and
// the answer flips on the very next request.
func TestRequireOrgWriteReadsTheMembershipRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newOrgWriteEnv(t)
	ctx := t.Context()
	guarded := env.guarded()

	user := env.users["user"]

	rec := httptest.NewRecorder()
	_ = guarded(rec, requestAs(user, env.org, http.MethodPost, false))
	r.Equal(http.StatusOK, rec.Code, "the fixture must start out allowed")

	dbSvc := env.authMw.dbService

	member, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, env.org.UID)
	r.NoError(err)

	viewer := models.MemberRoleViewer
	r.NoError(dbSvc.UpdateOrganizationMember(ctx, member.UID,
		models.OrganizationMemberUpdate{Role: &viewer}))

	rec = httptest.NewRecorder()
	_ = guarded(rec, requestAs(user, env.org, http.MethodPost, false))
	r.Equal(http.StatusForbidden, rec.Code,
		"a demotion must take effect on the next request, not at the next token refresh")
}
