package app

import (
	"context"
	"embed"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func helperRequest(method, target string) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), method, target, http.NoBody)
}

func TestHandlerWithDocsHostRedirectsOnlyTheDocsHost(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	})
	srv := &Server{
		config:  &config.Config{Server: config.ServerConfig{DocsHost: "Docs.Acme.com"}},
		handler: inner,
	}
	handler := srv.handlerWithDocsHost()

	redirected := httptest.NewRecorder()
	req := helperRequest(http.MethodGet, "/guide?x=1")
	req.Host = "docs.acme.com:443"
	handler.ServeHTTP(redirected, req)
	require.Equal(t, http.StatusFound, redirected.Code)
	require.Equal(t, "/docs/guide?x=1", redirected.Header().Get("Location"))

	already := httptest.NewRecorder()
	req = helperRequest(http.MethodGet, "/docs/guide")
	req.Host = "docs.acme.com"
	handler.ServeHTTP(already, req)
	require.Equal(t, http.StatusTeapot, already.Code)

	other := httptest.NewRecorder()
	req = helperRequest(http.MethodGet, "/guide")
	req.Host = "app.acme.com"
	handler.ServeHTTP(other, req)
	require.Equal(t, http.StatusTeapot, other.Code)

	plain := &Server{config: &config.Config{}, handler: inner}
	require.NotNil(t, plain.handlerWithDocsHost())
}

func TestServeFileMissingFileIsA404(t *testing.T) {
	t.Parallel()

	srv := &Server{config: &config.Config{}}
	rec := httptest.NewRecorder()

	err := srv.serveFile(embed.FS{}, "nope.txt")(rec, helperRequest(http.MethodGet, "/nope.txt"))
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestVersionAndHealthRegion(t *testing.T) {
	t.Parallel()

	srv := newHealthServer()
	srv.config.Node.Region = "eu-west"

	code, resp := getHealth(t, srv)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "eu-west", resp.Node.Region)

	rec := httptest.NewRecorder()
	require.NoError(t, srv.getVersion(rec, helperRequest(http.MethodGet, "/api/mgmt/version")))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "json")
	require.NotEmpty(t, rec.Body.String())
}

func TestServeLegacySPARedirectUnknownPathIs404(t *testing.T) {
	t.Parallel()

	srv := &Server{config: &config.Config{}}
	rec := httptest.NewRecorder()

	require.NoError(t, srv.serveLegacySPARedirect(rec, helperRequest(http.MethodGet, "/unrelated")))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeAppRedirectProxiesAndFallsBack(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		_, _ = writer.Write([]byte("upstream " + req.URL.Path))
	}))
	t.Cleanup(upstream.Close)

	srv := &Server{config: &config.Config{}}
	rule := config.RedirectRule{
		PathPrefix: "/dashboard",
		TargetHost: strings.TrimPrefix(upstream.URL, "http://"),
		TargetPath: "/app",
	}

	rec := httptest.NewRecorder()
	require.NoError(t, srv.serveAppRedirect(rec, helperRequest(http.MethodGet, "/dashboard/x"), rule, nil))
	require.Equal(t, "upstream /app/x", rec.Body.String())
	require.Equal(t, "solidping-dev", rec.Header().Get("X-Proxied-By"))

	// Dead upstream: the fallback serves the request instead of a 502.
	dead := rule
	dead.TargetHost = "127.0.0.1:1"
	fallbackRec := httptest.NewRecorder()
	fallback := func(writer http.ResponseWriter, _ *http.Request) error {
		_, err := writer.Write([]byte("embedded"))

		return err
	}
	require.NoError(t, srv.serveAppRedirect(fallbackRec, helperRequest(http.MethodGet, "/dashboard/x"), dead, fallback))
	require.Equal(t, "embedded", fallbackRec.Body.String())

	// A failing fallback becomes a 500.
	failing := func(http.ResponseWriter, *http.Request) error { return http.ErrAbortHandler }
	failRec := httptest.NewRecorder()
	require.NoError(t, srv.serveAppRedirect(failRec, helperRequest(http.MethodGet, "/dashboard/x"), dead, failing))
	require.Equal(t, http.StatusInternalServerError, failRec.Code)
}

func TestServeAppRootRoutes(t *testing.T) {
	t.Parallel()

	srv := &Server{config: &config.Config{Server: config.ServerConfig{
		Redirects: []config.RedirectRule{{PathPrefix: "/legacy", TargetHost: "127.0.0.1:1", TargetPath: "/legacy"}},
	}}}

	root := httptest.NewRecorder()
	require.NoError(t, srv.serveAppRoot(root, helperRequest(http.MethodGet, "/")))
	require.Equal(t, http.StatusFound, root.Code)
	require.Equal(t, config.DashboardBasePath+"/", root.Header().Get("Location"))

	api := httptest.NewRecorder()
	require.NoError(t, srv.serveAppRoot(api, helperRequest(http.MethodGet, "/api/nope")))
	require.Equal(t, http.StatusNotFound, api.Code)

	missing := httptest.NewRecorder()
	require.NoError(t, srv.serveAppRoot(missing, helperRequest(http.MethodGet, "/typo")))
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Contains(t, missing.Header().Get("Content-Type"), "text/html")
}

func TestProxyPostHog(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write([]byte("ph " + req.URL.Path + " " + req.Host))
	}))
	t.Cleanup(upstream.Close)

	off := &Server{config: &config.Config{}}
	rec := httptest.NewRecorder()
	require.NoError(t, off.proxyPostHog(rec, helperRequest(http.MethodPost, config.PostHogProxyPath+"/e/")))
	require.Equal(t, http.StatusNotFound, rec.Code)

	on := &Server{config: &config.Config{PostHog: config.PostHogConfig{
		Enabled: true, ProjectAPIKey: "phc_test", Host: upstream.URL,
	}}}
	rec = httptest.NewRecorder()
	require.NoError(t, on.proxyPostHog(rec, helperRequest(http.MethodPost, config.PostHogProxyPath+"/e/")))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "ph /e/ ")
	require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "upstream CORS headers are stripped")

	bad := &Server{config: &config.Config{PostHog: config.PostHogConfig{
		Enabled: true, ProjectAPIKey: "phc_test", Host: "http://[::1",
	}}}
	require.Error(t, bad.proxyPostHog(httptest.NewRecorder(), helperRequest(http.MethodPost, config.PostHogProxyPath+"/e/")))
}

func TestServeDocsFileFallsBackTo404(t *testing.T) {
	t.Parallel()

	srv := &Server{config: &config.Config{}}
	rec := httptest.NewRecorder()
	srv.serveDocsFile(rec, "no/such/page")
	require.Equal(t, http.StatusNotFound, rec.Code)
}
