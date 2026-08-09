package app

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
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// renameEnv is a fully wired server (real NewServer + real SetupRoutes over
// in-memory SQLite) on an httptest socket, with one seeded org that owns a
// public status page and a check — i.e. every public surface a customer can
// have pasted somewhere before the org is renamed.
type renameEnv struct {
	t         *testing.T
	server    *Server
	ts        *httptest.Server
	client    *http.Client
	authSvc   *auth.Service
	org       *models.Organization
	ownerUID  string
	pageSlug  string
	checkSlug string
}

func newRenameEnv(t *testing.T, orgSlug string) *renameEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "org-rename-redirect-secret"
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

	org := models.NewOrganization(orgSlug, "Acme")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	owner := models.NewUser("owner@" + orgSlug + ".example")
	r.NoError(server.dbService.CreateUser(ctx, owner))

	member := models.NewOrganizationMember(org.UID, owner.UID, models.MemberRoleOwner)
	now := time.Now()
	member.JoinedAt = &now
	r.NoError(server.dbService.CreateOrganizationMember(ctx, member))

	page := models.NewStatusPage(org.UID, "Public", "public")
	page.IsDefault = true
	r.NoError(server.dbService.CreateStatusPage(ctx, page))

	check := models.NewCheck(org.UID, "api", "http")
	checkName := "API"
	check.Name = &checkName
	r.NoError(server.dbService.CreateCheck(ctx, check))

	return &renameEnv{
		t: t, server: server, ts: ts,
		// Redirects must be observed, never followed.
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		authSvc:   server.authService,
		org:       org,
		ownerUID:  owner.UID,
		pageSlug:  page.Slug,
		checkSlug: *check.Slug,
	}
}

// get issues an unauthenticated GET and returns the status plus the Location
// header (empty when there is none).
func (e *renameEnv) get(path string) (int, string) {
	e.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.ts.URL+path, nil)
	require.NoError(e.t, err)

	resp, err := e.client.Do(req)
	require.NoError(e.t, err)

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, resp.Header.Get("Location")
}

// rename renames the seeded org through the real service path.
func (e *renameEnv) rename(newSlug string) *auth.OrgProfileResponse {
	e.t.Helper()

	slug := newSlug

	resp, err := e.authSvc.UpdateOrgProfile(context.Background(), e.org.Slug, e.ownerUID,
		auth.UpdateOrgProfileRequest{Slug: &slug}, auth.Context{})
	require.NoError(e.t, err)

	return resp
}

// publicSurfaces lists the URL shapes a customer can have pasted outside the
// dashboard, plus the dashboard's own org-scoped API and SPA URLs. They are
// what a rename must not break.
func publicSurfaces(env *renameEnv, slug string) map[string]string {
	return map[string]string{
		"status page view":    "/api/v1/status-pages/" + slug + "/" + env.pageSlug,
		"status page default": "/api/v1/status-pages/" + slug,
		"status page summary": "/api/v1/status-pages/" + slug + "/" + env.pageSlug + "/summary",
		"status page badge":   "/api/v1/status-pages/" + slug + "/" + env.pageSlug + "/badge",
		"status page feed":    "/api/v1/status-pages/" + slug + "/" + env.pageSlug + "/feed.xml",
		"check badge":         "/api/v1/orgs/" + slug + "/checks/" + env.checkSlug + "/badges/status",
		"org api":             "/api/v1/orgs/" + slug + "/checks",
		"status0 page":        "/status0/" + slug + "/" + env.pageSlug,
		"dash0 page":          "/dash0/orgs/" + slug + "/checks",
	}
}

// TestRenamedOrgPreviousSlugRedirects is the core promise of spec
// 2026-08-08-12: after a rename, every URL a customer had pasted somewhere
// still works — it permanently redirects to the same URL under the new slug.
//
// The positive control is the second half: the SAME paths under the NEW slug
// must be served directly (no redirect), proving the middleware is not blanket
// redirecting everything it sees.
func TestRenamedOrgPreviousSlugRedirects(t *testing.T) {
	t.Parallel()

	env := newRenameEnv(t, "acme-old")

	// Control: before the rename the old slug serves directly.
	for name, path := range publicSurfaces(env, "acme-old") {
		status, _ := env.get(path)
		require.NotEqual(t, http.StatusMovedPermanently, status,
			"%s must not redirect before the rename", name)
	}

	env.rename("acme-new")

	for name, path := range publicSurfaces(env, "acme-old") {
		status, location := env.get(path)
		require.Equalf(t, http.StatusMovedPermanently, status,
			"%s on the previous slug must permanently redirect (got %d)", name, status)
		require.Equal(t, publicSurfaces(env, "acme-new")[name], location,
			"%s must redirect to the same path under the new slug", name)
	}

	// Positive control: the same paths under the CURRENT slug are genuinely
	// served — 200 for the public surfaces, 401 for the authenticated org API
	// (which is the handler answering, not a redirect). Without this half, a
	// middleware that redirected everything unconditionally would still pass.
	for name, path := range publicSurfaces(env, "acme-new") {
		status, location := env.get(path)
		require.Equalf(t, directStatusFor(name), status,
			"%s on the current slug must be served, not redirected (Location %q)", name, location)
	}
}

// directStatusFor is the status a surface answers when it is NOT redirected.
// Everything public renders; the org API is the one authenticated route in the
// set, so an unauthenticated probe gets 401 from the auth middleware.
func directStatusFor(name string) int {
	if name == "org api" {
		return http.StatusUnauthorized
	}

	return http.StatusOK
}

// TestRenamedOrgEmbedWidgetSummaryRedirects isolates the surface with the
// hardest compatibility guarantee: the /embed/v1 widget is pasted verbatim into
// third-party pages and polls the status-page summary endpoint. If that
// endpoint stopped resolving after a rename, every embedded widget on the
// internet would go blank — so it must answer a redirect a browser `fetch`
// follows transparently, and the widget script itself must keep serving.
func TestRenamedOrgEmbedWidgetSummaryRedirects(t *testing.T) {
	t.Parallel()

	env := newRenameEnv(t, "widget-old")
	env.rename("widget-new")

	status, location := env.get("/api/v1/status-pages/widget-old/" + env.pageSlug + "/summary")
	require.Equal(t, http.StatusMovedPermanently, status)
	require.Equal(t, "/api/v1/status-pages/widget-new/"+env.pageSlug+"/summary", location)

	// The widget script is org-independent and must be untouched by any of this.
	widgetStatus, _ := env.get("/embed/v1/widget.js")
	require.NotEqual(t, http.StatusMovedPermanently, widgetStatus)
}

// TestRenamedOrgRedirectPreservesQueryAndMethod pins the two details that make
// the redirect usable in practice: query strings survive (badge styling,
// widget parameters), and a non-idempotent request gets 308 rather than 301 —
// a 301 licenses clients to replay a POST as a GET, which would silently drop
// a heartbeat's body.
func TestRenamedOrgRedirectPreservesQueryAndMethod(t *testing.T) {
	t.Parallel()

	env := newRenameEnv(t, "query-old")
	env.rename("query-new")

	status, location := env.get(
		"/api/v1/status-pages/query-old/" + env.pageSlug + "/badge?style=flat&label=up")
	require.Equal(t, http.StatusMovedPermanently, status)
	require.Equal(t, "/api/v1/status-pages/query-new/"+env.pageSlug+"/badge?style=flat&label=up", location)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.ts.URL+"/api/v1/heartbeat/query-old/some-check", nil)
	require.NoError(t, err)

	resp, err := env.client.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusPermanentRedirect, resp.StatusCode,
		"a POST must get 308 so the method and body survive the redirect")
	require.Equal(t, "/api/v1/heartbeat/query-new/some-check", resp.Header.Get("Location"))
}

// TestRenamedThenDeletedOrgIs404OnBothSlugs is the cross-spec boundary with
// 2026-08-08-11: a deleted org's slug 404s immediately, with no alias,
// tombstone or redirect. Renaming first must not open a back door — BOTH the
// current and the previous slug must be dead.
func TestRenamedThenDeletedOrgIs404OnBothSlugs(t *testing.T) {
	t.Parallel()

	env := newRenameEnv(t, "doomed-old")
	env.rename("doomed-new")

	// Control: the alias resolves while the org is alive.
	status, _ := env.get("/api/v1/status-pages/doomed-old/" + env.pageSlug)
	require.Equal(t, http.StatusMovedPermanently, status)

	require.NoError(t, env.authSvc.DeleteOrg(context.Background(), "doomed-new",
		auth.DeleteOrgRequest{Slug: "doomed-new"}))

	for _, slug := range []string{"doomed-old", "doomed-new"} {
		status, location := env.get("/api/v1/status-pages/" + slug + "/" + env.pageSlug)
		require.NotEqualf(t, http.StatusMovedPermanently, status,
			"a deleted org must not redirect from %q (Location %q)", slug, location)
		require.Equalf(t, http.StatusNotFound, status, "slug %q must 404 after deletion", slug)
	}
}

// TestPreviousSlugNeverShadowsLiveOrg pins the precedence rule: once another
// organization claims a freed slug, that slug serves the NEW org and the alias
// is gone — an alias must never resolve across tenants.
func TestPreviousSlugNeverShadowsLiveOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	env := newRenameEnv(t, "shared-slug")
	env.rename("renamed-away")

	// Control: the freed slug currently redirects to the renamed org.
	status, location := env.get("/api/v1/orgs/shared-slug/checks")
	r.Equal(http.StatusMovedPermanently, status)
	r.Equal("/api/v1/orgs/renamed-away/checks", location)

	claimant := models.NewUser("claimant@example.com")
	r.NoError(env.server.dbService.CreateUser(ctx, claimant))

	created, err := env.authSvc.CreateOrg(ctx, claimant.UID,
		auth.CreateOrgRequest{Name: "Newcomer", Slug: "shared-slug"}, auth.Context{})
	r.NoError(err)
	r.Equal("shared-slug", created.Slug)
	r.NotEqual(env.org.UID, created.UID)

	// The alias is released the moment the slug is claimed: no redirect, and
	// the slug now resolves to the brand-new organization.
	status, location = env.get("/api/v1/orgs/shared-slug/checks")
	r.NotEqual(http.StatusMovedPermanently, status,
		"a live org's slug must never redirect to a renamed org (Location %q)", location)

	resolved, err := env.server.dbService.GetOrganizationBySlug(ctx, "shared-slug")
	r.NoError(err)
	r.Equal(created.UID, resolved.UID)

	_, err = env.server.dbService.GetOrganizationByPreviousSlug(ctx, "shared-slug")
	r.Error(err, "the alias must be released once the slug is claimed")
}

// TestRenamedOrgRedirectRewritesOnlyTheOrgSegment is the aliasing trap: an org
// renamed away from "acme" that owns a check ALSO slugged "acme". Only the
// organization segment may be rewritten — a naive string replacement of the old
// slug in the path would corrupt the check identifier and 404 the badge.
func TestRenamedOrgRedirectRewritesOnlyTheOrgSegment(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	env := newRenameEnv(t, "collide")

	twin := models.NewCheck(env.org.UID, "collide", "http")
	twinName := "Collide"
	twin.Name = &twinName
	r.NoError(env.server.dbService.CreateCheck(ctx, twin))

	env.rename("collide-new")

	status, location := env.get("/api/v1/orgs/collide/checks/collide/badges/status")
	r.Equal(http.StatusMovedPermanently, status)
	r.Equal("/api/v1/orgs/collide-new/checks/collide/badges/status", location,
		"only the org segment may be rewritten; the identically named check must survive")
}

// wsRoutePattern is the one org-scoped route that deliberately cannot redirect:
// an HTTP 301/308 has no meaning in a WebSocket handshake, so a client on a
// previous slug must reconnect against the current one. Documented in
// wiki/api-specification/orgs.md.
const wsRoutePattern = "/api/v1/orgs/{org}/events/ws"

// concreteURLForPattern turns a chi route pattern into a requestable URL by
// substituting the org slug and filling every other parameter with a
// placeholder. The redirect runs ahead of every handler, so the placeholders
// never have to resolve to anything real.
func concreteURLForPattern(pattern, orgSlug string) (string, bool) {
	segments := strings.Split(pattern, "/")

	for i, segment := range segments {
		switch {
		case segment == "{org}":
			segments[i] = orgSlug
		case segment == "*":
			segments[i] = "x"
		case strings.HasPrefix(segment, "{"):
			segments[i] = "placeholder"
		}
	}

	return strings.Join(segments, "/"), true
}

// TestEveryOrgScopedAPIRouteRedirectsOnAPreviousSlug is the structural guard on
// the promise in wiki/api-specification/orgs.md that the redirect covers "the
// whole /api/v1/orgs/:org/... API".
//
// It walks the REAL route table rather than a hand-maintained list: the route
// registrations run to well over a thousand lines, and a new group built with
// `api.NewGroup("/orgs/:org/...")` instead of the orgGroup helper silently
// misses the middleware — invisible in review, and only ever hit by API/CLI
// clients holding an old slug (dash0 masks it, because its SPA-level redirect
// fires first).
func TestEveryOrgScopedAPIRouteRedirectsOnAPreviousSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newRenameEnv(t, "walked-old")
	env.rename("walked-new")

	var patterns []string

	r.NoError(env.server.router.Walk(func(method, pattern string) error {
		if method != http.MethodGet || !strings.HasPrefix(pattern, "/api/v1/orgs/{org}") {
			return nil
		}

		if pattern != wsRoutePattern {
			patterns = append(patterns, pattern)
		}

		return nil
	}))

	// The walk must actually find the route table — an empty list would make
	// every assertion below vacuously true.
	r.Greater(len(patterns), 20, "the org-scoped GET route table looks implausibly small")

	for _, pattern := range patterns {
		path, ok := concreteURLForPattern(pattern, "walked-old")
		r.True(ok, pattern)

		status, location := env.get(path)
		r.Equalf(http.StatusMovedPermanently, status,
			"%s must redirect on a previous slug (got %d)", pattern, status)

		wantPath, _ := concreteURLForPattern(pattern, "walked-new")
		r.Equalf(wantPath, location, "%s must redirect to the current slug", pattern)
	}
}

// TestOrgScopedRoutesOutsideOrgGroupRedirect pins, by name, the groups that are
// built with their own middleware chain instead of the orgGroup helper — the
// exact set an audit found uncovered. The walk test above would catch a
// regression too, but these names document WHY the chains differ (extra admin
// gate, config-as-code, the entitlements service-signature chain), so a future
// refactor sees the requirement rather than an anonymous failure.
func TestOrgScopedRoutesOutsideOrgGroupRedirect(t *testing.T) {
	t.Parallel()

	env := newRenameEnv(t, "outside-old")
	env.rename("outside-new")

	// path suffix -> the group it belongs to, for a legible failure message.
	routes := map[string]string{
		"/jobs":                        "jobs",
		"/jobs/stats":                  "jobs admin",
		"/admin/jobs":                  "jobs admin",
		"/checks/export":               "config-as-code (admin)",
		"/discovery/scans":             "discovery",
		"/private-regions":             "private locations",
		"/agent-enrollment-tokens":     "agent enrollment tokens",
		"/entitlements":                "entitlements (service-signature chain)",
		"/integrations/msteams/status": "msteams integration",
	}

	for suffix, group := range routes {
		status, location := env.get("/api/v1/orgs/outside-old" + suffix)
		require.Equalf(t, http.StatusMovedPermanently, status,
			"%s (%s) must redirect on a previous slug", suffix, group)
		require.Equal(t, "/api/v1/orgs/outside-new"+suffix, location)
	}
}

// TestEntitlementsRedirectLeavesTheSignedPathAlone pins the one ordering risk in
// wiring the redirect ahead of the entitlements service-auth chain: a signed
// request addressed to the CURRENT slug must still reach the signature
// verifier untouched. The redirect runs outermost, so it must be a pure
// pass-through here — if it swallowed or rewrote the request, a correctly
// signed billing-service call would break.
func TestEntitlementsRedirectLeavesTheSignedPathAlone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newRenameEnv(t, "billed-old")
	env.rename("billed-new")

	// Previous slug: permanently redirected, so the billing service learns the
	// canonical path (and re-signs for it — the signature covers the path).
	status, location := env.get("/api/v1/orgs/billed-old/entitlements")
	r.Equal(http.StatusMovedPermanently, status)
	r.Equal("/api/v1/orgs/billed-new/entitlements", location)

	// Current slug: not redirected — the request falls through to the
	// service-signature / auth chain, which answers 401 for this unsigned,
	// unauthenticated probe.
	status, location = env.get("/api/v1/orgs/billed-new/entitlements")
	r.NotEqual(http.StatusMovedPermanently, status,
		"a signed request on the current slug must reach the auth chain (Location %q)", location)
	r.Equal(http.StatusUnauthorized, status)
}
