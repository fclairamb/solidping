package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadServer(t *testing.T, sitemap string, sitemapCode int) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()

	var hits, logins atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/docs/sitemap.xml":
			if sitemapCode != 0 {
				w.WriteHeader(sitemapCode)

				return
			}

			_, _ = w.Write([]byte(sitemap))
		case r.URL.Path == "/api/v1/auth/login":
			logins.Add(1)
			_, _ = w.Write([]byte(`{"accessToken":"t"}`))
		case r.URL.Path == "/api/v1/test/checks/bulk":
			if strings.Contains(r.URL.RawQuery, "count=0") {
				w.WriteHeader(http.StatusTeapot)
				_, _ = w.Write([]byte(strings.Repeat("x", 400)))

				return
			}
		default:
			hits.Add(1)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, &hits, &logins
}

const sitemapXML = `<urlset>
<url><loc>http://docs.example/docs/a</loc></url>
<url><loc>/docs/b</loc></url>
<url><loc>http://nohost</loc></url>
<url><loc>relative</loc></url>
</urlset>`

func TestDocsPaths(t *testing.T) {
	t.Parallel()

	srv, _, _ := loadServer(t, sitemapXML, 0)
	paths, err := newClient(srv.URL).docsPaths(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"/docs/a", "/docs/b"}, paths)

	bad, _, _ := loadServer(t, "", http.StatusNotFound)
	_, err = newClient(bad.URL).docsPaths(t.Context())
	require.ErrorContains(t, err, "sitemap returned 404")

	garbage, _, _ := loadServer(t, "<urlset", 0)
	_, err = newClient(garbage.URL).docsPaths(t.Context())
	require.Error(t, err)

	_, err = newClient("http://127.0.0.1:1").docsPaths(t.Context())
	require.Error(t, err)
}

func TestBulkCreateChecks(t *testing.T) {
	t.Parallel()

	srv, _, _ := loadServer(t, "", 0)
	c := newClient(srv.URL)

	require.NoError(t, c.bulkCreateChecks(t.Context(), 5, "10s", "http://target"))

	err := c.bulkCreateChecks(t.Context(), 0, "10s", "http://target")
	require.ErrorContains(t, err, "bulk-create returned 418")
	assert.Contains(t, err.Error(), "…", "long error bodies are truncated")

	require.Error(t, newClient("http://127.0.0.1:1").bulkCreateChecks(t.Context(), 1, "1s", "x"))
}

func TestGet(t *testing.T) {
	t.Parallel()

	srv, hits, _ := loadServer(t, "", 0)
	require.NoError(t, newClient(srv.URL).get(t.Context(), "/anything"))
	assert.Equal(t, int32(1), hits.Load())
	require.Error(t, newClient("http://127.0.0.1:1").get(t.Context(), "/x"))
}

func TestScenarioLoads(t *testing.T) {
	t.Run("docs crawl fetches every sitemap path and caches the sitemap", func(t *testing.T) {
		srv, hits, _ := loadServer(t, sitemapXML, 0)
		c := newClient(srv.URL)

		require.NoError(t, docsCrawl(t.Context(), c))
		require.NoError(t, docsCrawl(t.Context(), c))
		assert.Equal(t, int32(4), hits.Load())
	})

	t.Run("docs crawl surfaces a sitemap failure", func(t *testing.T) {
		srv, _, _ := loadServer(t, "", http.StatusInternalServerError)
		require.Error(t, docsCrawl(t.Context(), newClient(srv.URL)))
	})

	t.Run("docs crawl stops when canceled", func(t *testing.T) {
		srv, hits, _ := loadServer(t, sitemapXML, 0)
		c := newClient(srv.URL)
		_, err := cachedDocsPaths(t.Context(), c)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.NoError(t, docsCrawl(ctx, c))
		assert.Zero(t, hits.Load())
	})

	t.Run("dash0 reload loads the shell 50 times", func(t *testing.T) {
		srv, hits, _ := loadServer(t, "", 0)
		require.NoError(t, dash0Reload(t.Context(), newClient(srv.URL)))
		assert.Equal(t, int32(50), hits.Load())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.NoError(t, dash0Reload(ctx, newClient(srv.URL)))
		assert.Equal(t, int32(50), hits.Load())
	})

	t.Run("login burst logs in once per goroutine", func(t *testing.T) {
		srv, _, logins := loadServer(t, "", 0)
		require.NoError(t, loginBurst(t.Context(), newClient(srv.URL)))
		assert.Equal(t, int32(12), logins.Load())
	})
}
