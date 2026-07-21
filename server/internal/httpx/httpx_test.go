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
