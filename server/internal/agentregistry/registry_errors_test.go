package agentregistry_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agentregistry"
)

// scriptedUpstream answers /token with tokenBody and every other path with
// handler, so a test can script one upstream misbehavior.
func scriptedUpstream(t *testing.T, tokenStatus int, tokenBody string, handler http.HandlerFunc) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(tokenStatus)
		_, _ = io.WriteString(w, tokenBody)
	})
	mux.HandleFunc("/v2/", handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server.URL
}

func TestDefaultsAndUnknownKind(t *testing.T) {
	t.Parallel()

	// Everything defaulted: no network is touched by these requests.
	reg := agentregistry.New(agentregistry.Config{})

	rec := do(t, reg, http.MethodGet, "/v2/solidping/tags/list")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "NAME_UNKNOWN", errorCode(t, rec))

	// No version func at all behaves as an untagged build.
	rec = do(t, reg, http.MethodGet, "/v2/solidping/manifests/current")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "MANIFEST_UNKNOWN", errorCode(t, rec))
}

func TestBlobUpstreamErrors(t *testing.T) {
	t.Parallel()

	digest := "/v2/solidping/blobs/sha256:" + strings.Repeat("d", 64)

	cases := map[string]struct {
		handler http.HandlerFunc
		status  int
		code    string
	}{
		"not found": {
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			status:  http.StatusNotFound,
			code:    "BLOB_UNKNOWN",
		},
		"no redirect": {
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			status:  http.StatusBadGateway,
			code:    "UNAVAILABLE",
		},
		"redirect without location": {
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTemporaryRedirect) },
			status:  http.StatusBadGateway,
			code:    "UNAVAILABLE",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			reg := newRegistry(scriptedUpstream(t, http.StatusOK, `{"token":"t"}`, tc.handler), "1.2.3")
			rec := do(t, reg, http.MethodGet, digest)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.code, errorCode(t, rec))
		})
	}
}

func TestManifestUpstreamMisbehavior(t *testing.T) {
	t.Parallel()

	cases := map[string]http.HandlerFunc{
		"no content type": func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Content-Type"] = nil
			_, _ = io.WriteString(w, "{}")
		},
		"too large": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", indexType)
			_, _ = w.Write(make([]byte, 4<<20+10))
		},
		"truncated body": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", indexType)
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "short")
		},
	}

	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			reg := newRegistry(scriptedUpstream(t, http.StatusOK, `{"token":"t"}`, handler), "1.2.3")
			rec := do(t, reg, http.MethodGet, "/v2/solidping/manifests/1.2.3")
			require.Equal(t, http.StatusBadGateway, rec.Code)
			require.Equal(t, "UNAVAILABLE", errorCode(t, rec))
		})
	}
}

func TestManifestByDigestMustMatchBody(t *testing.T) {
	t.Parallel()

	// The upstream answers with a different body than the digest asked for
	// and announces nothing, so only the registry's own check catches it.
	url := scriptedUpstream(t, http.StatusOK, `{"token":"t"}`, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", indexType)
		_, _ = io.WriteString(w, "{}")
	})

	rec := do(t, newRegistry(url, "1.2.3"), http.MethodGet, "/v2/solidping/manifests/sha256:"+strings.Repeat("e", 64))
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestManifestCachesAreBounded(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	url := scriptedUpstream(t, http.StatusOK, `{"token":"t"}`, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", indexType)
		_, _ = io.WriteString(w, req.URL.Path) // a distinct body per tag
	})

	reg := newRegistry(url, "1.2.3")

	// More distinct tags than either cache holds: all must still be served.
	for i := range 70 {
		rec := do(t, reg, http.MethodGet, fmt.Sprintf("/v2/solidping/manifests/1.0.%d", i))
		require.Equal(t, http.StatusOK, rec.Code, i)
		require.Contains(t, rec.Body.String(), fmt.Sprintf("1.0.%d", i))
	}

	require.Equal(t, int32(70), calls.Load())

	// The earliest tag was evicted: asking again goes upstream again.
	rec := do(t, reg, http.MethodGet, "/v2/solidping/manifests/1.0.0")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int32(71), calls.Load())
}

func TestTokenFailures(t *testing.T) {
	t.Parallel()

	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	cases := map[string]struct {
		status int
		body   string
	}{
		"token status": {http.StatusForbidden, ""},
		"invalid json": {http.StatusOK, "not json"},
		"empty token":  {http.StatusOK, `{}`},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			reg := newRegistry(scriptedUpstream(t, tc.status, tc.body, ok), "1.2.3")
			rec := do(t, reg, http.MethodGet, "/v2/solidping/manifests/1.2.3")
			require.Equal(t, http.StatusBadGateway, rec.Code)
		})
	}
}

func TestAccessTokenFieldAndRetryOn401(t *testing.T) {
	t.Parallel()

	var manifestCalls, tokenCalls atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		n := tokenCalls.Add(1)
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d"}`, n)
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, req *http.Request) {
		manifestCalls.Add(1)

		// The first token is stale: only the refreshed one is accepted.
		if req.Header.Get("Authorization") != "Bearer tok-2" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.Header().Set("Content-Type", indexType)
		_, _ = io.WriteString(w, "{}")
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	rec := do(t, newRegistry(server.URL, "1.2.3"), http.MethodGet, "/v2/solidping/manifests/1.2.3")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int32(2), tokenCalls.Load())
	require.Equal(t, int32(2), manifestCalls.Load())
}

func TestUpstreamConnectionDropAfterToken(t *testing.T) {
	t.Parallel()

	url := scriptedUpstream(t, http.StatusOK, `{"token":"t"}`, func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}

		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})

	rec := do(t, newRegistry(url, "1.2.3"), http.MethodGet, "/v2/solidping/manifests/1.2.3")
	require.Equal(t, http.StatusBadGateway, rec.Code)
}
