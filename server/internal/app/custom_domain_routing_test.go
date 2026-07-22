package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

func TestHostOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("status.acme.com", hostOnly("status.acme.com"))
	r.Equal("status.acme.com", hostOnly("status.acme.com:8080"))
	r.Equal("status.acme.com", hostOnly("Status.Acme.Com"))
	r.Equal("", hostOnly(""))
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
	}
	for _, p := range allowed {
		r.True(isCustomHostAPIAllowed(p), "expected allowed: %s", p)
	}

	denied := []string{
		"/api/v1/orgs/acme/checks",
		"/api/v1/orgs/acme/incidents",
		"/api/v1/auth/login",
		"/api/v1/orgs/acme/status-pages/uid",
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

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.io"
	cfg.Server.DocsHost = "docs.solidping.io"
	cfg.Server.CustomDomainCNAMETarget = "cname.solidping.io"

	server := &Server{
		config:            cfg,
		router:            router,
		customDomainCache: newCustomDomainCache(customDomainCacheTTL),
	}

	// status.acme.com resolves to a servable page; unknown.example.com is
	// negative-cached (not a custom domain).
	server.customDomainCache.set("status.acme.com", &resolvedCustomDomain{
		OrgSlug: "acme", Slug: "main", Name: "Acme Status",
	})
	server.customDomainCache.set("unknown.example.com", nil)

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

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			r.Equal(tc.wantStatus, rec.Code)
			if tc.wantBody != "" {
				r.Contains(rec.Body.String(), tc.wantBody)
			}
		})
	}
}
