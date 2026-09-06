package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// viewerEnv is a fully wired server (real NewServer + real SetupRoutes over
// in-memory SQLite) with one org and one member per role — the minimum needed
// to drive the write floor through the REAL route table.
//
// It is deliberately separate from demoEnv: the demo guard is an allowlist
// keyed on users.demo, this one is a role floor keyed on the membership row.
// The two are independent by design (different key, different shape, different
// failure modes), and sharing a fixture would quietly couple them.
type viewerEnv struct {
	t      *testing.T
	server *Server
	ts     *httptest.Server
	org    *models.Organization

	viewerToken string
	userToken   string
	viewerUser  *models.User
}

func newViewerEnv(t *testing.T) *viewerEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "viewer-guard-secret"
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

	org := models.NewOrganization("viewerguard", "Viewer Guard")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	now := time.Now()

	mkMember := func(email string, role models.MemberRole) *models.User {
		user := models.NewUser(email)
		r.NoError(server.dbService.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(server.dbService.CreateOrganizationMember(ctx, member))

		return user
	}

	viewerUser := mkMember("viewer@viewerguard.example", models.MemberRoleViewer)
	// The positive control is an ORDINARY member, not an owner: an owner would
	// sail past admin-only gates too, and the property under test is that the
	// floor admits the lowest role it is supposed to admit.
	plainUser := mkMember("user@viewerguard.example", models.MemberRoleUser)

	return &viewerEnv{
		t:          t,
		server:     server,
		ts:         ts,
		org:        org,
		viewerUser: viewerUser,
		viewerToken: mintTestToken(t, server, viewerUser.UID, org.Slug,
			string(models.MemberRoleViewer), false),
		userToken: mintTestToken(t, server, plainUser.UID, org.Slug,
			string(models.MemberRoleUser), false),
	}
}

// do issues one request and returns the status, the decoded error code and the
// decoded error title (both empty when the body is not an error document).
func (e *viewerEnv) do(method, path, token string) (int, string, string) {
	e.t.Helper()

	req, err := http.NewRequestWithContext(
		context.Background(), method, e.ts.URL+path, strings.NewReader("{}"))
	require.NoError(e.t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.ts.Client().Do(req)
	require.NoError(e.t, err)

	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Code  string `json:"code"`
		Title string `json:"title"`
	}

	_ = json.NewDecoder(resp.Body).Decode(&body)

	return resp.StatusCode, body.Code, body.Title
}

// viewerRoute is one entry of the real route table.
type viewerRoute struct{ method, pattern string }

func (r viewerRoute) key() string { return r.method + " " + r.pattern }

// viewerSelfScopedRoutes is the §2 allowlist, expressed here as the boundary the
// walk enforces. The exemption itself lives at REGISTRATION (orgGroupSelf in
// server.go), not in a path table inside the middleware — this map is how the
// test notices when that boundary moves, in either direction.
//
// The value is why a viewer may write there. Both reasons reduce to the same
// sentence: the write only ever touches the caller's own row.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const maps.
var viewerSelfScopedRoutes = map[string]string{
	"POST /api/v1/orgs/{org}/tokens": "the member's own PAT; it inherits their role, so it is bound by this same gate",

	"POST /api/v1/orgs/{org}/users/me/notification-contacts": "the member's own notification contact",
	"DELETE /api/v1/orgs/{org}/users/me/notification-contacts/{contactUid}": "the member's own " +
		"notification contact",
	"POST /api/v1/orgs/{org}/users/me/notification-contacts/{contactUid}/verify": "verifying the member's " +
		"own contact",
	"POST /api/v1/orgs/{org}/users/me/notification-contacts/{contactUid}/verify/confirm": "confirming the " +
		"member's own contact",
	"PATCH /api/v1/orgs/{org}/users/me/notification-routes/{routeUid}": "where the member themself gets paged",
	"POST /api/v1/orgs/{org}/users/me/notification-routes/{routeUid}/test": "a test notification to the " +
		"member's own contact",
	"POST /api/v1/orgs/{org}/users/me/telegram/link": "linking the member's own Telegram account",
}

// viewerPublicOrgRoutes names the org-scoped non-GET routes that are
// unauthenticated BY DESIGN and therefore never reach the write floor.
//
// It exists because the proof below must be exhaustive: every org-scoped
// non-GET route lands in exactly one of three buckets — this list, the
// self-scoped allowlist above, or a 403. Nothing is skipped, which is what
// turns "we added a middleware" into a property.
//
// The test asserts in both directions: an entry whose route has since been put
// behind RequireAuth fails (the entry would be hiding a guarded route), and an
// entry that matches no registered route fails (the list has rotted).
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const maps.
var viewerPublicOrgRoutes = map[string]string{
	"POST /api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers": "public status-page subscribe; " +
		"anyone on the internet may already call it",
}

// viewerStricterGateFragments are the denials that are NOT the write floor but
// are still correct answers to a viewer: the groups that spell out an admin or
// owner chain by hand — and the few handlers that make the same check inline —
// refuse a viewer because they refuse a `user` too.
//
// Matching these fragments case-insensitively, rather than accepting ANY 403,
// is what stops the walk from passing on an unrelated forbidden answer — a
// locked status page, a disabled feature, an entitlement cap — that would look
// identical from the status code alone. The fragments (not whole messages)
// absorb the handler-level variants such as "admin access required to manage
// source connections" without loosening what counts as a role denial.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var viewerStricterGateFragments = []string{
	"admin access required",
	"owner access required",
}

// isStricterRoleDenial reports whether a 403 body is one of the pre-existing
// admin/owner denials rather than the write floor.
func isStricterRoleDenial(title string) bool {
	lowered := strings.ToLower(title)
	for _, fragment := range viewerStricterGateFragments {
		if strings.Contains(lowered, fragment) {
			return true
		}
	}

	return false
}

// orgScopedWriteRoutes walks the REAL route table and returns every registered
// non-GET route under /api/v1/orgs/{org}. HEAD and OPTIONS are excluded for the
// same reason RequireOrgWrite passes them: they change nothing.
func orgScopedWriteRoutes(t *testing.T, env *viewerEnv) []viewerRoute {
	t.Helper()

	var routes []viewerRoute

	require.NoError(t, env.server.router.Walk(func(method, pattern string) error {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return nil
		}

		if !strings.HasPrefix(pattern, "/api/v1/orgs/{org}") {
			return nil
		}

		routes = append(routes, viewerRoute{method, pattern})

		return nil
	}))

	return routes
}

// TestEveryOrgScopedWriteRouteRefusesViewers is THE structural proof behind this
// spec.
//
// The hole it closes was never a missing check in one handler: it was that
// `viewer` and `user` were indistinguishable everywhere below RequireOrgAccess,
// so the exposed surface was "every non-GET route anyone ever registered
// through orgGroup". A test that named a few of them would re-create exactly
// the gap it was written to close.
//
// So this walks the REAL route table and sorts every registered org-scoped
// non-GET route into exactly one of three buckets:
//
//	(a) viewerPublicOrgRoutes — unauthenticated by design, never reaches the gate;
//	(b) viewerSelfScopedRoutes — the §2 exemptions, where a viewer writes only
//	    their own row;
//	(c) 403 FORBIDDEN, carrying either the write floor's message or a stricter
//	    admin/owner denial.
//
// The message, not merely the status, is the assertion: an unrelated 403 must
// not be able to masquerade as the gate working.
func TestEveryOrgScopedWriteRouteRefusesViewers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)
	routes := orgScopedWriteRoutes(t, env)

	// The walk must actually find the route table — an empty or tiny list
	// would make every assertion below vacuously true.
	r.Greater(len(routes), 60, "the org-scoped write route table looks implausibly small")

	floored, stricter, publicSeen, selfSeen := 0, 0, 0, 0

	for _, rt := range routes {
		path, ok := concreteURLForPattern(rt.pattern, env.org.Slug)
		r.True(ok, rt.pattern)

		switch {
		case isPublicOrgRoute(t, env, rt, path):
			publicSeen++
		case isViewerSelfScopedRoute(t, env, rt, path):
			selfSeen++
		default:
			if assertRefusedForViewer(r, env, rt, path) {
				floored++
			} else {
				stricter++
			}
		}
	}

	// Anti-vacuity floors. `floored` is the population this spec actually
	// changed; if the orgGroup helper stopped applying RequireOrgWrite, every
	// one of these would fall through to a 400/404 and the loop above would
	// fail — but a wiring mistake that made them all answer "Admin access
	// required" would not, so the floor is asserted separately.
	r.Greaterf(floored, 50,
		"only %d routes answered the write floor; is RequireOrgWrite still wired into orgGroup?", floored)
	r.Positive(stricter, "no admin/owner-gated write route was walked at all")

	r.Equalf(len(viewerSelfScopedRoutes), selfSeen,
		"viewerSelfScopedRoutes has %d entries but only %d matched a registered route — the list has rotted",
		len(viewerSelfScopedRoutes), selfSeen)
	r.Equalf(len(viewerPublicOrgRoutes), publicSeen,
		"viewerPublicOrgRoutes has %d entries but only %d matched a registered route — the list has rotted",
		len(viewerPublicOrgRoutes), publicSeen)
}

// TestNoOrgScopedWriteRouteRefusesOrdinaryUsers is the positive control, and it
// is not decoration: a gate that also locked out `user` would be worse than the
// hole it closes, and every assertion in the walk above would still pass.
//
// It repeats the identical walk as an ordinary member and asserts that no route
// answers the role denial. The routes may still fail for their own reasons — a
// bogus body, a missing resource, an admin-only gate — so the assertion is on
// the write floor's MESSAGE, which only RequireOrgWrite ever emits.
func TestNoOrgScopedWriteRouteRefusesOrdinaryUsers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)
	routes := orgScopedWriteRoutes(t, env)

	r.Greater(len(routes), 60, "the org-scoped write route table looks implausibly small")

	for _, rt := range routes {
		path, ok := concreteURLForPattern(rt.pattern, env.org.Slug)
		r.True(ok, rt.pattern)

		_, _, title := env.do(rt.method, path, env.userToken)
		r.NotEqualf(middleware.ViewerWriteMessage, title,
			"%s refused an ordinary member as if they were a viewer", rt.key())
	}
}

// TestViewerSelfScopedRoutesAreNotRefusedByTheWriteGate is the other half of the
// §2 boundary: the exempt routes must get PAST the gate. They may still fail for
// their own reasons (an empty body, a contact that does not exist) — what they
// must never answer is the write floor, which would mean a viewer cannot
// configure where they themselves get paged.
func TestViewerSelfScopedRoutesAreNotRefusedByTheWriteGate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)

	for key := range viewerSelfScopedRoutes {
		method, pattern, ok := strings.Cut(key, " ")
		r.True(ok, key)

		path, ok := concreteURLForPattern(pattern, env.org.Slug)
		r.True(ok, pattern)

		_, _, title := env.do(method, path, env.viewerToken)
		r.NotEqualf(middleware.ViewerWriteMessage, title,
			"%s is self-scoped but the write floor refused it", key)
	}
}

// TestOrgScopedWriteGroupsOutsideOrgGroupAreFloored pins, by name, the groups
// that are built with their own middleware chain instead of the orgGroup helper.
// The walk above would catch a regression too, but these names document WHY
// those chains differ — an extra admin gate, the entitlements service-signature
// chain — so a future refactor sees the requirement rather than an anonymous
// failure.
func TestOrgScopedWriteGroupsOutsideOrgGroupAreFloored(t *testing.T) {
	t.Parallel()

	env := newViewerEnv(t)

	// method+suffix -> the group it belongs to, for a legible failure message.
	routes := map[[2]string]string{
		{http.MethodPut, "/entitlements"}:                    "entitlements (service-signature chain, admin-user fallback)",
		{http.MethodPatch, "/entitlements"}:                  "entitlements (service-signature chain, admin-user fallback)",
		{http.MethodPost, "/members"}:                        "members admin",
		{http.MethodPost, "/checks/import"}:                  "config-as-code (admin)",
		{http.MethodPost, "/discovery/scans"}:                "discovery (admin)",
		{http.MethodPost, "/private-regions"}:                "private locations (admin)",
		{http.MethodPost, "/agent-enrollment-tokens"}:        "agent enrollment tokens (admin)",
		{http.MethodPost, "/integrations/msteams/link-code"}: "msteams org integration",
		{http.MethodPost, "/integrations/slack/install-url"}: "slack org integration",
		{http.MethodDelete, "/jobs/placeholder"}:             "background jobs (cancel)",
	}

	for route, group := range routes {
		method, suffix := route[0], route[1]

		status, code, _ := env.do(method, "/api/v1/orgs/"+env.org.Slug+suffix, env.viewerToken)
		require.Equalf(t, http.StatusForbidden, status,
			"%s %s (%s) must refuse a viewer", method, suffix, group)
		require.Equalf(t, string(base.ErrorCodeForbidden), code,
			"%s %s (%s) must refuse a viewer with FORBIDDEN", method, suffix, group)
	}
}

// TestViewerPATCannotWrite is the credential half of "membership row, not
// claims": a PAT is a long-lived credential minted outside any session, and it
// must be bound by the same floor.
//
// The GET is a positive control, not decoration: without it, a PAT that was
// simply broken would produce the refusal on the POST and prove nothing.
func TestViewerPATCannotWrite(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)
	ctx := context.Background()

	pat := auth.PATTokenPrefix + "viewerguardpatfixturevalue000001"
	token := models.NewUserToken(env.viewerUser.UID, &env.org.UID, pat, models.TokenTypePAT)
	token.Properties = models.JSONMap{"name": "viewer guard fixture"}
	r.NoError(env.server.dbService.CreateUserToken(ctx, token))

	status, code, _ := env.do(http.MethodGet, "/api/v1/orgs/"+env.org.Slug+"/checks", pat)
	r.Equalf(http.StatusOK, status, "the viewer PAT must authenticate a GET (got %d/%s)", status, code)

	status, code, title := env.do(http.MethodPost, "/api/v1/orgs/"+env.org.Slug+"/checks", pat)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeForbidden), code)
	r.Equal(middleware.ViewerWriteMessage, title)
}

// TestDemotionTakesEffectOnTheNextRequest is why the gate reads the membership
// row instead of claims.Role. A member demoted to viewer keeps a perfectly
// valid token — minted while they were a `user`, and still saying so — until it
// expires. If the gate trusted the token, the demotion would not take effect
// until the next refresh, which is precisely when an admin needs it most.
func TestDemotionTakesEffectOnTheNextRequest(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)
	ctx := context.Background()

	path := "/api/v1/orgs/" + env.org.Slug + "/checks"

	status, _, _ := env.do(http.MethodPost, path, env.userToken)
	r.NotEqual(http.StatusForbidden, status, "the fixture must start out allowed to write")

	user, err := env.server.dbService.GetUserByEmail(ctx, "user@viewerguard.example")
	r.NoError(err)

	member, err := env.server.dbService.GetMemberByUserAndOrg(ctx, user.UID, env.org.UID)
	r.NoError(err)

	viewer := models.MemberRoleViewer
	r.NoError(env.server.dbService.UpdateOrganizationMember(ctx, member.UID,
		models.OrganizationMemberUpdate{Role: &viewer}))

	// Same token, unchanged, still claiming Role: "user".
	status, code, title := env.do(http.MethodPost, path, env.userToken)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeForbidden), code)
	r.Equal(middleware.ViewerWriteMessage, title)
}

// TestViewersCanStillRead is the sanity floor under the whole change: the role
// is called viewer, and it has to keep viewing.
func TestViewersCanStillRead(t *testing.T) {
	t.Parallel()

	env := newViewerEnv(t)

	for _, suffix := range []string{"/checks", "/incidents", "/status-pages", "/labels", "/members"} {
		status, code, _ := env.do(http.MethodGet, "/api/v1/orgs/"+env.org.Slug+suffix, env.viewerToken)
		require.Equalf(t, http.StatusOK, status, "GET %s must stay open to a viewer (got %d/%s)",
			suffix, status, code)
	}
}

// isPublicOrgRoute reports whether the route is on the named by-design-public
// list, and proves the entry is still earned: a listed route that has since been
// put behind RequireAuth would be hiding a guarded route, so it fails.
func isPublicOrgRoute(t *testing.T, env *viewerEnv, rt viewerRoute, path string) bool {
	t.Helper()

	reason, listed := viewerPublicOrgRoutes[rt.key()]
	if !listed {
		return false
	}

	anonStatus, anonCode, _ := env.do(rt.method, path, "")
	require.Falsef(t, anonStatus == http.StatusUnauthorized && anonCode == string(base.ErrorCodeNoToken),
		"%s is listed as unauthenticated (%s) but now sits behind RequireAuth — drop the entry so the floor covers it",
		rt.key(), reason)

	return true
}

// isViewerSelfScopedRoute reports whether the route is one of the §2 exemptions,
// and proves the exemption is real rather than merely declared: a listed route
// that now answers the write floor would mean the registration moved back to
// orgGroup while this table still claimed otherwise.
func isViewerSelfScopedRoute(t *testing.T, env *viewerEnv, rt viewerRoute, path string) bool {
	t.Helper()

	reason, listed := viewerSelfScopedRoutes[rt.key()]
	if !listed {
		return false
	}

	_, _, title := env.do(rt.method, path, env.viewerToken)
	require.NotEqualf(t, middleware.ViewerWriteMessage, title,
		"%s is listed as self-scoped (%s) but the write floor refused it", rt.key(), reason)

	return true
}

// assertRefusedForViewer is bucket (c): everything not named above must answer
// 403 FORBIDDEN to a viewer, carrying either the write floor's message or one of
// the stricter admin/owner denials. It reports whether the write floor was the
// one that answered, so the caller can assert a non-trivial population for each.
func assertRefusedForViewer(r *require.Assertions, env *viewerEnv, rt viewerRoute, path string) bool {
	status, code, title := env.do(rt.method, path, env.viewerToken)

	r.Equalf(http.StatusForbidden, status,
		"%s must refuse a viewer (got %d/%s: %q); if it is public by design, name it in viewerPublicOrgRoutes",
		rt.key(), status, code, title)
	r.Equalf(string(base.ErrorCodeForbidden), code,
		"%s must refuse a viewer with FORBIDDEN (got %s)", rt.key(), code)

	if title == middleware.ViewerWriteMessage {
		return true
	}

	r.Truef(isStricterRoleDenial(title),
		"%s answered 403 with an unexpected message %q — an unrelated denial must not stand in for the gate",
		rt.key(), title)

	return false
}
