package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// newTestStatus0FS returns a minimal but real HTML shell with a </head>
// anchor, so injectStatus0Meta has something to splice into. CI substitutes a
// one-line "placeholder" file for the real (gitignored) status0res/index.html
// when running backend-only jobs, which has no </head> at all — using that
// real path here would make the sp-page assertions below environment-dependent.
func newTestStatus0FS() fstest.MapFS {
	return fstest.MapFS{
		"status0res/index.html": &fstest.MapFile{
			Data: []byte("<!doctype html><html><head><title>x</title></head><body></body></html>"),
		},
		// A real asset, so status0StaticAssetExists has a file to find and the
		// asset-vs-SPA-route split is exercised rather than assumed.
		"status0res/assets/app-abc123.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
}

func TestHostOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("status.acme.com", hostOnly("status.acme.com"))
	r.Equal("status.acme.com", hostOnly("status.acme.com:8080"))
	r.Equal("status.acme.com", hostOnly("Status.Acme.Com"))
	r.Empty(hostOnly(""))
}

func TestIsCustomHostAPIAllowed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	allowed := []string{
		"/api/v1/status-pages/acme",
		"/api/v1/status-pages/acme/main",
		"/api/v1/status-pages/acme/main/feed.xml",
		"/api/v1/public/status-subscribers/confirm",
		"/api/v1/orgs/acme/status-pages/uid/subscribers",
		"/api/v1/orgs/acme/checks/web/badges/uptime",
		// The status page footer renders the build it is talking to.
		"/api/mgmt/version",
		"/api/mgmt/health",
	}
	for _, p := range allowed {
		r.True(isCustomHostAPIAllowed(p), "expected allowed: %s", p)
	}

	denied := []string{
		"/api/v1/orgs/acme/checks",
		"/api/v1/orgs/acme/incidents",
		"/api/v1/auth/login",
		"/api/v1/orgs/acme/status-pages/uid",
		// /api/mgmt is allowlisted by EXACT path, never by prefix — these
		// siblings must not ride along. /memory and /scheduling are
		// super-admin; /limits and /report are simply not the SPA's business.
		"/api/mgmt/memory",
		"/api/mgmt/scheduling/cost-distribution",
		"/api/mgmt/limits",
		"/api/mgmt/report",
		"/api/mgmt/version/extra",
		"/api/mgmt",
	}
	for _, p := range denied {
		r.False(isCustomHostAPIAllowed(p), "expected denied: %s", p)
	}
}

func TestIsCustomHostForbidden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, p := range []string{"/dash0", "/dash0/x", "/docs", "/docs/x", "/openapi", "/openapi.yaml", "/metrics"} {
		r.True(isCustomHostForbidden(p), "expected forbidden: %s", p)
	}

	for _, p := range []string{"/", "/status0", "/status0/assets/a.js", "/api/v1/status-pages/acme"} {
		r.False(isCustomHostForbidden(p), "expected not forbidden: %s", p)
	}
}

// newCustomHostTestServer builds a minimal Server exercising only the
// custom-domain wrapper: a marker router (for allowlisted API passthrough), a
// pre-seeded resolution cache (so no DB is touched), and a config that pins the
// reserved hosts.
func newCustomHostTestServer(t *testing.T) *Server {
	t.Helper()

	router := httpx.New()
	main := router.NewGroup("")
	api := main.NewGroup("/api/v1")
	api.GET("/status-pages/:org/:slug", func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api-view"))

		return nil
	})

	mgmt := main.NewGroup("/api/mgmt")
	mgmt.GET("/version", func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api-version"))

		return nil
	})

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.io"
	cfg.Server.DocsHost = "docs.solidping.io"
	cfg.Server.CustomDomainCNAMETarget = "cname.solidping.io"

	server := &Server{
		config:            cfg,
		router:            router,
		customDomainCache: newCustomDomainCache(customDomainCacheTTL),
		status0FS:         newTestStatus0FS(),
	}

	// status.acme.com resolves to a servable page; unknown.example.com is
	// negative-cached (not a custom domain).
	server.customDomainCache.set("status.acme.com", &resolvedCustomDomain{
		OrgSlug: "acme", Slug: "main", Name: "Acme Status",
		Visibility: models.StatusPageVisibilityPublic,
	})
	server.customDomainCache.set("unknown.example.com", nil)

	// A password-protected page IS routed on its custom domain (the unlock
	// form has to appear somewhere) — its shell must not be shared-cacheable.
	server.customDomainCache.set("locked.acme.com", &resolvedCustomDomain{
		OrgSlug: "acme", Slug: "locked", Name: "Acme Internal",
		Visibility: models.StatusPageVisibilityPassword,
	})

	return server
}

func TestHandlerWithCustomDomains(t *testing.T) {
	t.Parallel()

	server := newCustomHostTestServer(t)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("next"))
	})
	handler := server.handlerWithCustomDomains(next)

	tests := []struct {
		name        string
		host        string
		path        string
		wantStatus  int
		wantBody    string // substring; "" skips the body assertion
		wantNotBody string
	}{
		{
			name: "custom host root serves status0 index with sp-page",
			host: "status.acme.com", path: "/",
			wantStatus: http.StatusOK, wantBody: `name="sp-page" content="acme/main"`,
		},
		{
			name: "custom host allowlisted API passes to router",
			host: "status.acme.com", path: "/api/v1/status-pages/acme/main",
			wantStatus: http.StatusOK, wantBody: "api-view",
		},
		{
			name: "custom host denied API is 404",
			host: "status.acme.com", path: "/api/v1/orgs/acme/checks",
			wantStatus: http.StatusNotFound,
		},
		{
			// Regression: this used to serve the bare shell (no sp-page), so the
			// SPA rendered its generic "visit a specific status page" landing.
			name: "custom host /status0/ serves the index WITH sp-page",
			host: "status.acme.com", path: "/status0/",
			wantStatus: http.StatusOK, wantBody: `name="sp-page" content="acme/main"`,
		},
		{
			name: "custom host /status0 (no slash) serves the index with sp-page",
			host: "status.acme.com", path: "/status0",
			wantStatus: http.StatusOK, wantBody: `content="acme/main"`,
		},
		{
			name: "custom host /status0/index.html is an entry point, not an asset",
			host: "status.acme.com", path: "/status0/index.html",
			wantStatus: http.StatusOK, wantBody: `content="acme/main"`,
		},
		{
			name: "custom host deep SPA route under /status0 still gets sp-page",
			host: "status.acme.com", path: "/status0/acme/main",
			wantStatus: http.StatusOK, wantBody: `content="acme/main"`,
		},
		{
			name: "custom host real asset is served raw, without sp-page",
			host: "status.acme.com", path: "/status0/assets/app-abc123.js",
			wantStatus: http.StatusOK, wantBody: "console.log(1)", wantNotBody: "sp-page",
		},
		{
			name: "custom host version endpoint reaches the router",
			host: "status.acme.com", path: "/api/mgmt/version",
			wantStatus: http.StatusOK, wantBody: "api-version",
		},
		{
			name: "custom host dash0 is 404",
			host: "status.acme.com", path: "/dash0",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "custom host with port still resolves",
			host: "status.acme.com:443", path: "/",
			wantStatus: http.StatusOK, wantBody: `content="acme/main"`,
		},
		{
			name: "custom host case-insensitive",
			host: "STATUS.ACME.COM", path: "/",
			wantStatus: http.StatusOK, wantBody: `content="acme/main"`,
		},
		{
			name: "reserved base-url host falls through to next",
			host: "solidping.io", path: "/",
			wantStatus: http.StatusOK, wantBody: "next",
		},
		{
			name: "reserved docs host falls through to next",
			host: "docs.solidping.io", path: "/",
			wantStatus: http.StatusOK, wantBody: "next",
		},
		{
			name: "unknown host falls through to next",
			host: "unknown.example.com", path: "/",
			wantStatus: http.StatusOK, wantBody: "next",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			r.Equal(tc.wantStatus, rec.Code)
			if tc.wantBody != "" {
				r.Contains(rec.Body.String(), tc.wantBody)
			}

			if tc.wantNotBody != "" {
				r.NotContains(rec.Body.String(), tc.wantNotBody)
			}
		})
	}
}

// TestCustomHostShellCacheControl covers the SPA shell served on a custom
// domain, which embeds the page's name and description as OG metadata. It used
// to send `public, max-age=60` for every resolved host, including the
// password-protected ones — so a shared cache in front of a customer's status
// domain could hand a gated page's identity to anyone who asked
// (spec 2026-08-22-06).
func TestCustomHostShellCacheControl(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	server := newCustomHostTestServer(t)

	handler := server.handlerWithCustomDomains(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	openRec := httptest.NewRecorder()
	openReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	openReq.Host = "status.acme.com"
	handler.ServeHTTP(openRec, openReq)

	r.Equal(http.StatusOK, openRec.Code)
	r.Equal("public, max-age=60", openRec.Header().Get("Cache-Control"))
	r.Equal("Cookie, X-Forwarded-Proto", openRec.Header().Get("Vary"))

	lockedRec := httptest.NewRecorder()
	lockedReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	lockedReq.Host = "locked.acme.com"
	handler.ServeHTTP(lockedRec, lockedReq)

	// Still served — the visitor needs the unlock form — but never stored.
	r.Equal(http.StatusOK, lockedRec.Code)
	r.Equal("private, no-store", lockedRec.Header().Get("Cache-Control"))
	r.NotContains(lockedRec.Header().Get("Cache-Control"), "public")
}

// TestLookupCustomDomainCarriesVisibility closes the gap the test above cannot
// see: it seeds its own cache entries, so a resolver that forgot to copy the
// page's visibility would still look right there. This one goes through the DB
// resolution path, where a missing copy would silently mark every custom
// domain as gated (or, if the default ever flipped, every gated one as public).
func TestLookupCustomDomainCarriesVisibility(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	server := &Server{dbService: dbSvc}
	verifiedAt := time.Now().UTC()

	testCases := []struct {
		name       string
		slug       string
		domain     string
		visibility string
	}{
		{name: "public page", slug: "open", domain: "status.acme.com",
			visibility: models.StatusPageVisibilityPublic},
		{name: "password page", slug: "locked", domain: "locked.acme.com",
			visibility: models.StatusPageVisibilityPassword},
	}

	for _, testCase := range testCases {
		page := models.NewStatusPage(org.UID, testCase.name, testCase.slug)
		page.Visibility = testCase.visibility

		if testCase.visibility == models.StatusPageVisibilityPassword {
			hash := "not-a-real-hash-but-non-empty"
			page.PasswordHash = &hash
		}

		r.NoError(dbSvc.CreateStatusPage(ctx, page))
		r.NoError(dbSvc.UpdateStatusPageCustomDomain(ctx, page.UID,
			&models.StatusPageCustomDomainUpdate{Domain: &testCase.domain, VerifiedAt: &verifiedAt}))

		resolved := server.lookupCustomDomain(ctx, testCase.domain)
		r.NotNil(resolved, testCase.name)
		r.Equal(testCase.visibility, resolved.Visibility, testCase.name)
	}
}
