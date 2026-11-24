package checkhttp_test

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
)

// socketServer serves handler on a Unix socket and returns its path (short
// directory: macOS caps socket paths at ~104 bytes).
func socketServer(t *testing.T, handler http.Handler) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "sph")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "s.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)

	server := &http.Server{Handler: handler} //nolint:gosec // test server
	go func() { _ = server.Serve(listener) }()

	t.Cleanup(func() { _ = server.Close() })

	return path
}

func runSocketCheck(t *testing.T, path string, extra map[string]any) *checkerdef.Result {
	t.Helper()

	cfgMap := map[string]any{"url": "http://app.local/health", "unix_socket": path}
	for key, value := range extra {
		cfgMap[key] = value
	}

	cfg := &checkhttp.HTTPConfig{}
	require.NoError(t, cfg.FromMap(cfgMap))

	ctx := checkerdef.WithLocalSocketAllowlist(t.Context(), []string{filepath.Dir(path) + "/"})

	result, err := (&checkhttp.HTTPChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestSocketStatusBodyAndHostHeader(t *testing.T) {
	t.Parallel()

	var gotHost, gotPath string

	path := socketServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		if r.URL.Query().Get("fail") != "" {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		_, _ = w.Write([]byte("ok-body"))
	}))

	t.Run("200 is up with the url host as Host header", func(t *testing.T) {
		result := runSocketCheck(t, path, nil)
		require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
		require.Equal(t, "app.local", gotHost)
		require.Equal(t, "/health", gotPath)
	})

	t.Run("500 is down", func(t *testing.T) {
		result := runSocketCheck(t, path, map[string]any{"url": "http://app.local/health?fail=1"})
		require.Equal(t, checkerdef.StatusDown, result.Status, result.Output)
	})

	t.Run("body assertion miss is down", func(t *testing.T) {
		result := runSocketCheck(t, path, map[string]any{"body_expect": "nope"})
		require.Equal(t, checkerdef.StatusDown, result.Status, result.Output)
	})

	t.Run("body assertion hit is up", func(t *testing.T) {
		result := runSocketCheck(t, path, map[string]any{"body_expect": "ok-body"})
		require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
	})

	t.Run("path outside the allowlist is refused", func(t *testing.T) {
		cfg := &checkhttp.HTTPConfig{}
		require.NoError(t, cfg.FromMap(map[string]any{"url": "http://app.local/", "unix_socket": path}))

		result, err := (&checkhttp.HTTPChecker{}).Execute(t.Context(), cfg)
		require.NoError(t, err)
		require.NotEqual(t, checkerdef.StatusUp, result.Status)
	})
}

func TestSocketRedirects(t *testing.T) {
	t.Parallel()

	var otherHostHits int

	path := socketServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "http://app.local/final", http.StatusFound)
		case "/other":
			http.Redirect(w, r, "http://other.example/final", http.StatusFound)
		default:
			if r.Host != "app.local" {
				otherHostHits++
			}

			_, _ = w.Write([]byte("final"))
		}
	}))

	same := runSocketCheck(t, path, map[string]any{"url": "http://app.local/same"})
	require.Equal(t, checkerdef.StatusUp, same.Status, same.Output)

	other := runSocketCheck(t, path, map[string]any{"url": "http://app.local/other"})
	require.NotEqual(t, checkerdef.StatusUp, other.Status, other.Output)
	require.Contains(t, other.Output[checkerdef.OutputKeyError], "redirect to different host refused")
	require.Zero(t, otherHostHits)
}
