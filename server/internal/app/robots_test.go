package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServeRobotsTxt: only solidping.io (any case, port or trailing dot) may
// be indexed; every other host is told to crawl nothing.
func TestServeRobotsTxt(t *testing.T) {
	t.Parallel()

	s := &Server{}

	for host, want := range map[string]string{
		"solidping.io":           robotsAllow,
		"SolidPing.io:443":       robotsAllow,
		"solidping.io.":          robotsAllow,
		"www.solidping.io":       robotsDisallow,
		"status.solidping.io":    robotsDisallow,
		"solidping.k8xp.com":     robotsDisallow,
		"monitor.example.com":    robotsDisallow,
		"localhost:4000":         robotsDisallow,
		"10.0.0.5":               robotsDisallow,
		"solidping.io.evil.test": robotsDisallow,
	} {
		r := require.New(t)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/robots.txt", http.NoBody)
		req.Host = host
		w := httptest.NewRecorder()
		r.NoError(s.serveRobotsTxt(w, req))

		resp := w.Result()
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		r.NoError(err)
		r.Equal(http.StatusOK, resp.StatusCode, "host %s", host)
		r.Equal("text/plain; charset=utf-8", resp.Header.Get("Content-Type"), "host %s", host)
		r.Equal(want, string(body), "host %s", host)
	}
}
