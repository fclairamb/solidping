package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/httpx"
)

// TestRouteMatchingPrecedence locks in the routing behavior the route table in
// server.go depends on after the bunrouter -> chi migration: static-vs-param
// precedence, the /docs vs /docs/* and dash0/status0 static split, org-scoped
// API routes winning over the SPA catch-all, the OPTIONS /api/v1/* CORS
// catch-all, and 404/405 shapes. It registers marker handlers at the same
// patterns server.go uses so a regression in matching (not handler content)
// surfaces here.
func TestRouteMatchingPrecedence(t *testing.T) {
	t.Parallel()

	// marker writes its tag as the body so the test can assert which route won.
	marker := func(tag string) httpx.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(tag))

			return nil
		}
	}

	router := httpx.New()
	main := router.NewGroup("")
	api := main.NewGroup("/api/v1")

	api.OPTIONS("/*path", marker("options-catchall"))
	api.GET("/auth/providers", marker("providers"))
	api.GET("/orgs/:org/checks", marker("checks-list"))
	api.GET("/orgs/:org/checks/:checkUid", marker("check-get"))
	// static /orgs/:org/checks/export must win over the /:checkUid param sibling.
	api.GET("/orgs/:org/checks/export", marker("checks-export"))

	main.GET("/docs", marker("docs-root"))
	main.GET("/docs/*path", marker("docs-wild"))
	main.GET("/dash0", marker("dash0-root"))
	main.GET("/dash0/*path", marker("dash0-wild"))
	main.GET("/status0", marker("status0-root"))
	main.GET("/status0/*path", marker("status0-wild"))
	main.GET("/*path", marker("app-catchall"))

	// want is the matched marker tag; wantStatus defaults to 200 when want set.
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		want       string
	}{
		{name: "docs root exact", method: http.MethodGet, path: "/docs", want: "docs-root"},
		{name: "docs subpath", method: http.MethodGet, path: "/docs/guide/intro", want: "docs-wild"},
		{name: "docs trailing slash", method: http.MethodGet, path: "/docs/", want: "docs-wild"},
		{name: "dash0 root", method: http.MethodGet, path: "/dash0", want: "dash0-root"},
		{name: "dash0 spa path", method: http.MethodGet, path: "/dash0/orgs/acme", want: "dash0-wild"},
		{name: "status0 root", method: http.MethodGet, path: "/status0", want: "status0-root"},
		{name: "status0 page", method: http.MethodGet, path: "/status0/acme", want: "status0-wild"},
		{name: "api list beats catchall", method: http.MethodGet, path: "/api/v1/orgs/acme/checks", want: "checks-list"},
		{name: "api param route", method: http.MethodGet, path: "/api/v1/orgs/acme/checks/x1", want: "check-get"},
		{name: "static beats param", method: http.MethodGet, path: "/api/v1/orgs/acme/checks/export", want: "checks-export"},
		{name: "public api route", method: http.MethodGet, path: "/api/v1/auth/providers", want: "providers"},
		{name: "spa catchall", method: http.MethodGet, path: "/some/spa/route", want: "app-catchall"},
		{name: "root hits catchall", method: http.MethodGet, path: "/", want: "app-catchall"},
		{name: "unknown GET under api falls to catchall", method: http.MethodGet, path: "/api/v1/nope", want: "app-catchall"},
		{name: "options preflight", method: http.MethodOptions, path: "/api/v1/orgs/acme/checks", want: "options-catchall"},
		{name: "options on unknown api path", method: http.MethodOptions, path: "/api/v1/whatever", want: "options-catchall"},
		{name: "post to GET-only route is 405", method: http.MethodPost, path: "/api/v1/orgs/acme/checks", wantStatus: 405},
		{name: "post to unknown path is 405 not 404", method: http.MethodPost, path: "/api/v1/nope", wantStatus: 405},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req := httptest.NewRequestWithContext(t.Context(), testCase.method, testCase.path, http.NoBody)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			wantStatus := testCase.wantStatus
			if wantStatus == 0 {
				wantStatus = http.StatusOK
			}
			r.Equal(wantStatus, rec.Code, "path=%s", testCase.path)
			if testCase.want != "" {
				r.Equal(testCase.want, rec.Body.String())
			}
		})
	}
}
