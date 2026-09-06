package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// demoEnv is a fully wired server (real NewServer + real SetupRoutes over
// in-memory SQLite) with one org, one demo user and one ordinary user — the
// minimum needed to drive the write guard through the REAL route table.
type demoEnv struct {
	t          *testing.T
	server     *Server
	ts         *httptest.Server
	org        *models.Organization
	demoToken  string
	plainToken string
}

func newDemoEnv(t *testing.T) *demoEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "demo-guard-secret"
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

	org := models.NewOrganization("demoguard", "Demo Guard")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	now := time.Now()

	demoUser := models.NewUser("demo@demoguard.example")
	demoUser.Demo = true
	r.NoError(server.dbService.CreateUser(ctx, demoUser))

	demoMember := models.NewOrganizationMember(org.UID, demoUser.UID, models.MemberRoleUser)
	demoMember.JoinedAt = &now
	r.NoError(server.dbService.CreateOrganizationMember(ctx, demoMember))

	plainUser := models.NewUser("owner@demoguard.example")
	r.NoError(server.dbService.CreateUser(ctx, plainUser))

	plainMember := models.NewOrganizationMember(org.UID, plainUser.UID, models.MemberRoleOwner)
	plainMember.JoinedAt = &now
	r.NoError(server.dbService.CreateOrganizationMember(ctx, plainMember))

	return &demoEnv{
		t:      t,
		server: server,
		ts:     ts,
		org:    org,
		demoToken: mintTestToken(t, server, demoUser.UID, org.Slug,
			string(models.MemberRoleUser), true),
		plainToken: mintTestToken(t, server, plainUser.UID, org.Slug,
			string(models.MemberRoleOwner), false),
	}
}

// mintTestToken signs an access token directly with the server's own JWT
// secret, so the test does not depend on a login flow — and, crucially, can
// mint a token whose `demo` claim is deliberately ABSENT for a user whose row
// says otherwise (see TestDemoGuardIgnoresAStaleNonDemoClaim).
func mintTestToken(t *testing.T, server *Server, userUID, orgSlug, role string, demo bool) string {
	t.Helper()

	now := time.Now()
	claims := &auth.Claims{
		UserUID: userUID,
		OrgSlug: orgSlug,
		Role:    role,
		Demo:    demo,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "solidping",
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte(server.config.Auth.JWTSecret))
	require.NoError(t, err)

	return signed
}

// do issues one authenticated request and returns the status and the decoded
// error code (empty when the body is not an error document).
func (e *demoEnv) do(method, path, token string) (int, string) {
	e.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, strings.NewReader("{}"))
	require.NoError(e.t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.ts.Client().Do(req)
	require.NoError(e.t, err)

	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Code string `json:"code"`
	}

	_ = json.NewDecoder(resp.Body).Decode(&body)

	return resp.StatusCode, body.Code
}

// TestEveryNonGETRouteIsClosedToADemoSession is THE structural proof behind the
// spec's "allowlist, never denylist" decision.
//
// It walks the REAL route table — not a hand-maintained list — and asserts that
// a demo session is refused with 403 DEMO_READ_ONLY on every registered
// AUTHENTICATED non-GET route except the ones the allowlist names. That is what
// turns "we listed four things" into a property: a mutating endpoint added next
// year is covered on the day it is registered, with no reviewer having to
// remember this feature exists.
//
// "Authenticated" is established empirically rather than by a hand-kept exempt
// list: each route is first probed with NO credential, and only the ones that
// answer 401 are behind RequireAuth. A route that is public (login, the device
// grant, the public status-page surfaces) never reaches the guard, and a demo
// credential buys nothing there that an anonymous caller did not already have.
func TestEveryNonGETRouteIsClosedToADemoSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDemoEnv(t)

	allowed := map[string]struct{}{}
	for _, entry := range auth.DemoAllowedRoutes() {
		allowed[entry[0]+" "+entry[1]] = struct{}{}
	}

	type route struct{ method, pattern string }

	var routes []route

	r.NoError(env.server.router.Walk(func(method, pattern string) error {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return nil
		}

		// Only the versioned REST API runs through RequireAuth. The rest of
		// the tree (static assets, /pub, the SPA fallbacks) has its own,
		// unauthenticated story.
		if !strings.HasPrefix(pattern, "/api/v1/") {
			return nil
		}

		routes = append(routes, route{method, pattern})

		return nil
	}))

	// The walk must actually find the route table — an empty list would make
	// every assertion below vacuously true.
	r.Greater(len(routes), 50, "the non-GET API route table looks implausibly small")

	guarded := 0

	for _, rt := range routes {
		if _, ok := allowed[rt.method+" "+rt.pattern]; ok {
			continue
		}

		path, ok := concreteURLForPattern(rt.pattern, env.org.Slug)
		r.True(ok, rt.pattern)

		// RequireAuth's fingerprint: a credential-less request to a route in
		// its chain answers 401 NO_TOKEN, and nothing else in the codebase
		// produces that pair. Probing for it identifies the authenticated
		// route set empirically, so this test needs no hand-kept list of
		// public routes to stay honest as the route table grows.
		anonStatus, anonCode := env.do(rt.method, path, "")
		if anonStatus != http.StatusUnauthorized || anonCode != string(base.ErrorCodeNoToken) {
			continue
		}

		status, code := env.do(rt.method, path, env.demoToken)

		// A route on a DIFFERENT credential scheme (the agent key, the
		// service signature) rejects our user JWT before the guard can speak.
		// It is closed to a demo session either way, which is what matters.
		if status == http.StatusUnauthorized {
			continue
		}

		r.Equalf(http.StatusForbidden, status,
			"%s %s must refuse a demo session (got %d)", rt.method, rt.pattern, status)
		r.Equalf(string(base.ErrorCodeDemoReadOnly), code,
			"%s %s must refuse a demo session with DEMO_READ_ONLY", rt.method, rt.pattern)

		guarded++
	}

	// Without this, a server where nothing is authenticated at all would make
	// every assertion above vacuous.
	r.Greaterf(guarded, 40,
		"only %d authenticated non-GET routes were probed; is the route table wired?", guarded)
}

// TestDemoAllowlistedRoutesAreNotRefusedByTheGuard is the other half: the six
// allowlisted routes must get PAST the guard. They may still fail for their own
// reasons (a missing body, a check that does not exist) — what they must never
// answer is DEMO_READ_ONLY, which would mean the demo cannot do the one thing it
// exists to do.
func TestDemoAllowlistedRoutesAreNotRefusedByTheGuard(t *testing.T) {
	t.Parallel()

	env := newDemoEnv(t)

	for _, entry := range auth.DemoAllowedRoutes() {
		method, pattern := entry[0], entry[1]

		path, ok := concreteURLForPattern(pattern, env.org.Slug)
		require.True(t, ok, pattern)

		_, code := env.do(method, path, env.demoToken)
		require.NotEqualf(t, string(base.ErrorCodeDemoReadOnly), code,
			"%s %s is allowlisted but the guard refused it", method, pattern)
	}
}

// TestNonDemoSessionsAreUntouchedByTheGuard pins the first rule: a session that
// is not a demo session never sees DEMO_READ_ONLY, whatever it calls.
func TestNonDemoSessionsAreUntouchedByTheGuard(t *testing.T) {
	t.Parallel()

	env := newDemoEnv(t)

	for _, path := range []string{
		"/api/v1/orgs/" + env.org.Slug + "/tokens",
		"/api/v1/orgs/" + env.org.Slug + "/integrations",
		"/api/v1/orgs/" + env.org.Slug + "/status-pages",
	} {
		_, code := env.do(http.MethodPost, path, env.plainToken)
		require.NotEqualf(t, string(base.ErrorCodeDemoReadOnly), code,
			"%s refused an ordinary session as if it were a demo", path)
	}
}

// TestDemoGuardIgnoresAStaleNonDemoClaim is the belt-and-braces rule: a token
// minted WITHOUT the demo claim for a user whose row says users.demo is still
// guarded, because RequireAuth re-derives the flag from the user row it loads
// on every request.
//
// This is what makes the guard independent of the mint sites being exhaustive —
// a claims site added later and forgotten cannot produce an unguarded demo
// session.
func TestDemoGuardIgnoresAStaleNonDemoClaim(t *testing.T) {
	t.Parallel()

	env := newDemoEnv(t)
	ctx := context.Background()

	user, err := env.server.dbService.GetUserByEmail(ctx, "demo@demoguard.example")
	require.NoError(t, err)

	stale := mintTestToken(t, env.server, user.UID, env.org.Slug, string(models.MemberRoleUser), false)

	status, code := env.do(http.MethodPost, "/api/v1/orgs/"+env.org.Slug+"/status-pages", stale)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, string(base.ErrorCodeDemoReadOnly), code)
}
