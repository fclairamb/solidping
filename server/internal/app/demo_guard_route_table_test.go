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

// demoRoute is one entry of the real route table.
type demoRoute struct{ method, pattern string }

func (r demoRoute) key() string { return r.method + " " + r.pattern }

// demoUnauthenticatedRoutes is the small, NAMED list of non-GET /api/v1 routes
// that are unauthenticated BY DESIGN, and therefore never reach the demo guard.
//
// It exists because the guard proof below must be exhaustive. The first version
// of that proof identified the authenticated route set empirically, by probing
// each route anonymously and keeping the ones that answered 401 NO_TOKEN.
// That fails safe but under-covers: any RequireAuth route whose anonymous probe
// happened to answer something else — a 404 or a 422 raised by a middleware
// running ahead of the auth check — was silently skipped, and a route that
// slipped the guard for that reason would have been reported as a pass.
//
// So every non-GET route now lands in exactly one of three buckets: this list,
// the write allowlist, or a 403 DEMO_READ_ONLY. Adding an entry here is a
// deliberate, reviewable act — the value is why it is safe for an anonymous
// caller, which is the same reason it is safe for a demo one.
//
// The test also asserts in both directions: an entry whose route has become
// authenticated fails (the entry is now hiding a guarded route), and an entry
// whose route no longer exists fails (the list has rotted).
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const maps.
var demoUnauthenticatedRoutes = map[string]string{
	// Credential schemes that are not the user JWT. A demo session presenting
	// its bearer here is rejected as an unknown agent key, exactly as an
	// anonymous caller is.
	"POST /api/v1/agent/attachments": "deported-agent key, not a user session",

	// The login funnel. Every one of these is how a caller BECOMES
	// authenticated, so requiring authentication would be circular.
	"POST /api/v1/auth/login":                  "login itself — the demo's front door",
	"POST /api/v1/auth/register":               "self-service registration",
	"POST /api/v1/auth/confirm-registration":   "consumes an emailed registration token",
	"POST /api/v1/auth/accept-invite":          "consumes an emailed invitation token",
	"POST /api/v1/auth/refresh":                "presents a refresh token, not an access token",
	"POST /api/v1/auth/request-password-reset": "anonymous by definition",
	"POST /api/v1/auth/reset-password": "consumes an emailed reset token; refuses demo " +
		"users in the handler (TestResetPasswordRefusesADemoUser)",
	"POST /api/v1/auth/2fa/verify":            "completes a login with a pending-2FA token",
	"POST /api/v1/auth/2fa/recovery":          "completes a login with a recovery code",
	"POST /api/v1/auth/passkeys/login/begin":  "passkey login challenge",
	"POST /api/v1/auth/passkeys/login/finish": "passkey login assertion",
	"POST /api/v1/auth/device":                "RFC 8628 device-code request",
	"POST /api/v1/auth/device/token":          "RFC 8628 device-code polling",
	"POST /api/v1/oauth/authorize":            "OAuth authorization request",
	"POST /api/v1/oauth/token":                "OAuth token exchange",
	"POST /api/v1/oauth/register":             "RFC 7591 dynamic client registration",
	"POST /api/v1/oauth/revoke":               "RFC 7009 revocation, authenticated by the token itself",

	// Inbound webhooks. Authenticated by a provider signature or a secret in
	// the path, never by a session.
	"POST /api/v1/integrations/slack/command":        "Slack webhook (signature-verified)",
	"POST /api/v1/integrations/slack/events":         "Slack webhook (signature-verified)",
	"POST /api/v1/integrations/slack/interaction":    "Slack webhook (signature-verified)",
	"POST /api/v1/integrations/discord/interactions": "Discord webhook (signature-verified)",
	"POST /api/v1/integrations/msteams/messages":     "MS Teams webhook",
	"POST /api/v1/integrations/twilio/message":       "Twilio webhook (signature-verified)",
	"POST /api/v1/integrations/twilio/status":        "Twilio webhook (signature-verified)",
	"POST /api/v1/integrations/twilio/voice":         "Twilio webhook (signature-verified)",
	"POST /api/v1/integrations/twilio/voice/gather":  "Twilio webhook (signature-verified)",
	"POST /api/v1/integrations/ovhsms/dlr/{token}":   "OVH delivery receipt, secret in the path",

	// Public product surfaces. Anyone on the internet may already call these,
	// so a demo session gains nothing by being able to.
	"POST /api/v1/heartbeat/{org}/{identifier}":                        "heartbeat ping ingestion, token in the body",
	"POST /api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers": "public status-page subscribe",
	"POST /api/v1/status-pages/{org}/unlock":                           "public status-page password unlock",
	"POST /api/v1/status-pages/{org}/{slug}/unlock":                    "public status-page password unlock",
}

// nonGETAPIRoutes walks the REAL route table and returns every registered
// non-GET route under /api/v1. The rest of the tree (static assets, /pub, the
// SPA fallbacks) has its own, unauthenticated story.
func nonGETAPIRoutes(t *testing.T, env *demoEnv) []demoRoute {
	t.Helper()

	var routes []demoRoute

	require.NoError(t, env.server.router.Walk(func(method, pattern string) error {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return nil
		}

		if !strings.HasPrefix(pattern, "/api/v1/") {
			return nil
		}

		routes = append(routes, demoRoute{method, pattern})

		return nil
	}))

	return routes
}

// TestEveryNonGETRouteIsClosedToADemoSession is THE structural proof behind the
// spec's "allowlist, never denylist" decision.
//
// It walks the REAL route table — not a hand-maintained list — and sorts every
// registered non-GET API route into exactly one of three buckets:
//
//	(a) demoUnauthenticatedRoutes — public by design, never reaches the guard;
//	(b) the write allowlist — the handful of things a demo session may do;
//	(c) refused with 403 DEMO_READ_ONLY.
//
// Nothing is skipped. That is what turns "we listed four things" into a
// property: a mutating endpoint added next year is covered on the day it is
// registered, with no reviewer having to remember this feature exists, and a
// route that slips the guard FAILS this test instead of being passed over.
func TestEveryNonGETRouteIsClosedToADemoSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDemoEnv(t)

	allowed := map[string]struct{}{}
	for _, entry := range auth.DemoAllowedRoutes() {
		allowed[entry[0]+" "+entry[1]] = struct{}{}
	}

	routes := nonGETAPIRoutes(t, env)

	// The walk must actually find the route table — an empty list would make
	// every assertion below vacuously true.
	r.Greater(len(routes), 50, "the non-GET API route table looks implausibly small")

	guarded, publicSeen, allowedSeen := 0, 0, 0

	for _, rt := range routes {
		path, ok := concreteURLForPattern(rt.pattern, env.org.Slug)
		r.True(ok, rt.pattern)

		switch {
		case isAllowedDemoRoute(allowed, rt):
			allowedSeen++
		case isPublicDemoRoute(t, env, rt, path):
			publicSeen++
		default:
			assertRefusedForDemo(r, env, rt, path)

			guarded++
		}
	}

	// Anti-vacuity floors: a server where nothing is authenticated at all, or
	// where the allowlist swallowed the table, would make the loop above
	// meaningless.
	r.Greaterf(guarded, 40,
		"only %d authenticated non-GET routes were refused; is the route table wired?", guarded)
	r.Equal(len(auth.DemoAllowedRoutes()), allowedSeen, "an allowlisted route is not registered any more")
	r.Equalf(len(demoUnauthenticatedRoutes), publicSeen,
		"demoUnauthenticatedRoutes has %d entries but only %d matched a registered route — the list has rotted",
		len(demoUnauthenticatedRoutes), publicSeen)
}

func isAllowedDemoRoute(allowed map[string]struct{}, rt demoRoute) bool {
	_, ok := allowed[rt.key()]

	return ok
}

// isPublicDemoRoute reports whether the route is on the named by-design-public
// list, and proves the entry is still earned: a listed route that has since
// been put behind RequireAuth would be hiding a guarded route, so it fails.
func isPublicDemoRoute(t *testing.T, env *demoEnv, rt demoRoute, path string) bool {
	t.Helper()

	reason, listed := demoUnauthenticatedRoutes[rt.key()]
	if !listed {
		return false
	}

	anonStatus, anonCode := env.do(rt.method, path, "")
	require.Falsef(t, anonStatus == http.StatusUnauthorized && anonCode == string(base.ErrorCodeNoToken),
		"%s is listed as unauthenticated (%s) but now sits behind RequireAuth — drop the entry so the guard covers it",
		rt.key(), reason)

	return true
}

// assertRefusedForDemo is bucket (c): everything not named above must answer
// 403 DEMO_READ_ONLY to a demo session.
func assertRefusedForDemo(r *require.Assertions, env *demoEnv, rt demoRoute, path string) {
	status, code := env.do(rt.method, path, env.demoToken)

	r.Equalf(http.StatusForbidden, status,
		"%s must refuse a demo session (got %d/%s); if it is public by design, name it in demoUnauthenticatedRoutes",
		rt.key(), status, code)
	r.Equalf(string(base.ErrorCodeDemoReadOnly), code,
		"%s must refuse a demo session with DEMO_READ_ONLY (got %s)", rt.key(), code)
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

// TestPATDerivedDemoClaimsAreGuardedToo is the test §9 names by that phrase.
//
// A personal access token belonging to the demo user IS a demo session: the
// claims ValidatePATToken mints carry Demo from the user row, so the same
// allowlist applies. That is what would make a PUBLISHED demo API key safe —
// the design allows one, and this is the assertion the design rests on.
//
// The GET half is a positive control, not decoration: without it, a PAT that
// was simply broken (revoked, mistyped, rejected by the credential parser)
// would produce a refusal on the POST and pass a test that asserted only the
// refusal, proving nothing about the guard.
func TestPATDerivedDemoClaimsAreGuardedToo(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDemoEnv(t)
	ctx := context.Background()

	demoUser, err := env.server.dbService.GetUserByEmail(ctx, "demo@demoguard.example")
	r.NoError(err)
	r.True(demoUser.Demo, "fixture user must carry users.demo")

	pat := auth.PATTokenPrefix + "demoguardpatfixturevalue00000001"
	token := models.NewUserToken(demoUser.UID, &env.org.UID, pat, models.TokenTypePAT)
	token.Properties = models.JSONMap{"name": "demo guard fixture"}
	r.NoError(env.server.dbService.CreateUserToken(ctx, token))

	// Positive control: the PAT is a working credential on a read.
	status, code := env.do(http.MethodGet, "/api/v1/orgs/"+env.org.Slug+"/checks", pat)
	r.Equalf(http.StatusOK, status, "the demo PAT must authenticate a GET (got %d/%s)", status, code)

	// The guard: a write off the same PAT is refused, exactly as it is off a
	// session JWT.
	status, code = env.do(http.MethodPost, "/api/v1/orgs/"+env.org.Slug+"/status-pages", pat)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeDemoReadOnly), code)

	// And the allowlist still applies to it: a PAT-driven demo session may
	// create a check, which is the whole point of publishing one.
	_, code = env.do(http.MethodPost, "/api/v1/orgs/"+env.org.Slug+"/checks/validate", pat)
	r.NotEqual(string(base.ErrorCodeDemoReadOnly), code)
}
