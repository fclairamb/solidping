package httpx_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/httpx"
)

var errBoom = errors.New("boom")

func newReq(t *testing.T, method, path string) *http.Request {
	t.Helper()

	return httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)
}

func ok(w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)

	return nil
}

// TestConvertPatternAndParams verifies bunrouter-style patterns are translated
// to chi and that path parameters are readable via httpx.Param.
func TestConvertPatternAndParams(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := httpx.New()
	var gotOrg, gotUID, gotWildcard string
	router.GET("/api/v1/orgs/:org/checks/:uid", func(w http.ResponseWriter, req *http.Request) error {
		gotOrg = httpx.Param(req, "org")
		gotUID = httpx.Param(req, "uid")
		w.WriteHeader(http.StatusOK)

		return nil
	})
	router.GET("/docs/*path", func(w http.ResponseWriter, req *http.Request) error {
		gotWildcard = httpx.Param(req, "*")
		w.WriteHeader(http.StatusOK)

		return nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodGet, "/api/v1/orgs/acme/checks/abc-123"))
	r.Equal(http.StatusOK, rec.Code)
	r.Equal("acme", gotOrg)
	r.Equal("abc-123", gotUID)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodGet, "/docs/guide/intro"))
	r.Equal(http.StatusOK, rec.Code)
	r.Equal("guide/intro", gotWildcard)
}

// TestStaticBeatsParam covers the static-segment-vs-param precedence the route
// table relies on (e.g. /tokens/current registered ahead of /tokens/:tokenUid).
func TestStaticBeatsParam(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := httpx.New()
	hits := ""
	router.DELETE("/tokens/current", func(w http.ResponseWriter, _ *http.Request) error {
		hits = "current"
		w.WriteHeader(http.StatusOK)

		return nil
	})
	router.DELETE("/tokens/:tokenUid", func(w http.ResponseWriter, req *http.Request) error {
		hits = "param:" + httpx.Param(req, "tokenUid")
		w.WriteHeader(http.StatusOK)

		return nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodDelete, "/tokens/current"))
	r.Equal("current", hits)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodDelete, "/tokens/xyz"))
	r.Equal("param:xyz", hits)
	r.Equal(http.StatusOK, rec.Code)
}

// TestDifferingParamNamesCoexist proves chi keeps each route's own param name,
// so /checks/:checkUid, /checks/:slug and /checks/:check/deps coexist and read
// back the correct name — the property that lets the migration skip renaming.
func TestDifferingParamNamesCoexist(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := httpx.New()
	group := router.NewGroup("/checks")
	var got string
	group.GET("/:checkUid", func(w http.ResponseWriter, req *http.Request) error {
		got = "get:" + httpx.Param(req, "checkUid")
		w.WriteHeader(http.StatusOK)

		return nil
	})
	group.PUT("/:slug", func(w http.ResponseWriter, req *http.Request) error {
		got = "put:" + httpx.Param(req, "slug")
		w.WriteHeader(http.StatusOK)

		return nil
	})
	group.GET("/:check/deps", func(w http.ResponseWriter, req *http.Request) error {
		got = "deps:" + httpx.Param(req, "check")
		w.WriteHeader(http.StatusOK)

		return nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodGet, "/checks/aaa"))
	r.Equal("get:aaa", got)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodPut, "/checks/bbb"))
	r.Equal("put:bbb", got)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodGet, "/checks/ccc/deps"))
	r.Equal("deps:ccc", got)
}

// TestMethodNotAllowed verifies a known path with an unregistered method yields
// 405, not 404.
func TestMethodNotAllowed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := httpx.New()
	router.GET("/thing", ok)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodPost, "/thing"))
	r.Equal(http.StatusMethodNotAllowed, rec.Code)
}

// TestMiddlewareOrderAndErrorFlow verifies middleware run outermost-first (in
// the order added) and that a handler's returned error propagates back up the
// chain for observation, while Wrap discards it at the top.
func TestMiddlewareOrderAndErrorFlow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var order []string
	var observed error
	mw := func(tag string) httpx.Middleware {
		return func(next httpx.HandlerFunc) httpx.HandlerFunc {
			return func(w http.ResponseWriter, req *http.Request) error {
				order = append(order, tag)
				err := next(w, req)
				if tag == "outer" {
					observed = err
				}

				return err
			}
		}
	}

	router := httpx.New()
	group := router.Use(mw("outer")).Use(mw("inner"))
	group.GET("/x", func(w http.ResponseWriter, _ *http.Request) error {
		order = append(order, "handler")
		w.WriteHeader(http.StatusTeapot)

		return errBoom
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodGet, "/x"))

	r.Equal([]string{"outer", "inner", "handler"}, order)
	r.Equal(errBoom, observed)
	r.Equal(http.StatusTeapot, rec.Code)
}

// TestGroupPrefixNesting verifies NewGroup composes path prefixes and inherits
// the parent middleware stack.
func TestGroupPrefixNesting(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	hits := 0
	router := httpx.New()
	api := router.Use(func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) error {
			hits++

			return next(w, req)
		}
	}).NewGroup("/api/v1")
	orgs := api.NewGroup("/orgs/:org")
	orgs.GET("/checks", ok)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newReq(t, http.MethodGet, "/api/v1/orgs/acme/checks"))
	r.Equal(http.StatusOK, rec.Code)
	r.Equal(1, hits)
}

// TestDuplicateRouteRegistrationPanics is the guard that the old post-hoc
// uniqueness check could not be.
//
// chi stores handlers in a map keyed by method and `setEndpoint` OVERWRITES
// that entry, so walking the finished tree can never reveal a duplicate — the
// loser is gone before Walk runs. The check therefore has to happen at
// registration time, and this test pins that it does, in the two shapes the
// route table actually produces: the same group twice, and two DIFFERENT groups
// resolving to the same pattern (which is how the real collision happened —
// an authenticated group and a public one both landing on
// `/api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers`).
func TestDuplicateRouteRegistrationPanics(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	noop := func(http.ResponseWriter, *http.Request) error { return nil }

	t.Run("same group", func(t *testing.T) {
		t.Parallel()

		router := httpx.New()
		group := router.NewGroup("/api/v1/things")
		group.POST("/:uid/subscribers", noop)

		var caught httpx.DuplicateRouteError

		func() {
			defer func() {
				recovered := recover()
				require.NotNil(t, recovered, "a duplicate registration must panic")
				require.ErrorAs(t, recovered.(error), &caught) //nolint:forcetypeassert // panic value is our error
			}()

			group.POST("/:uid/subscribers", noop)
		}()

		require.Equal(t, http.MethodPost, caught.Route.Method)
		require.Equal(t, "/api/v1/things/{uid}/subscribers", caught.Route.Pattern)
		require.Contains(t, caught.Error(), "unreachable")
	})

	t.Run("different groups, same resolved pattern", func(t *testing.T) {
		t.Parallel()

		router := httpx.New()
		authed := router.NewGroup("/api/v1/orgs/:org").Use(func(next httpx.HandlerFunc) httpx.HandlerFunc {
			return next
		})
		public := router.NewGroup("/api/v1")

		authed.POST("/status-pages/:uid/subscribers", noop)

		require.Panics(t, func() {
			public.POST("/orgs/:org/status-pages/:uid/subscribers", noop)
		}, "a different group is still the same pattern on the same mux")
	})

	// Negative control: the two patterns this spec actually ships coexist, so
	// the panics above are about duplication and not about the paths.
	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/status-pages")
	r.NotPanics(func() {
		group.POST("/:uid/subscribers", noop)
		group.POST("/:uid/subscribers/endpoints", noop)
		group.GET("/:uid/subscribers", noop)
	})
}

// TestRegisteredRoutesReportsWhatWasAsked pins the difference between the two
// views of the table: RegisteredRoutes is what the code asked for, Walk is what
// chi ended up matching.
func TestRegisteredRoutesReportsWhatWasAsked(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	noop := func(http.ResponseWriter, *http.Request) error { return nil }

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org")
	group.GET("/checks", noop)
	group.POST("/checks", noop)
	group.GET("/checks/:uid", noop)

	registered := router.RegisteredRoutes()
	r.Len(registered, 3)

	// Sorted by pattern, then method.
	r.Equal(httpx.Route{Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/checks"}, registered[0])
	r.Equal(httpx.Route{Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/checks"}, registered[1])
	r.Equal(httpx.Route{Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/checks/{uid}"}, registered[2])

	walked := 0
	r.NoError(router.Walk(func(string, string) error {
		walked++

		return nil
	}))
	r.Equal(len(registered), walked, "with no duplicates the two views must agree")
}
