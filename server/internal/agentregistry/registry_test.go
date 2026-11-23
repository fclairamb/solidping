package agentregistry_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agentregistry"
)

const (
	indexType = "application/vnd.oci.image.index.v1+json"
	testToken = "anon-token"
	blobCDN   = "https://cdn.example.com/blob?sig=abc"
)

// fakeUpstream is a ghcr look-alike: an anonymous token endpoint, one tagged
// manifest, and blobs that redirect to a signed CDN URL.
type fakeUpstream struct {
	server         *httptest.Server
	manifest       []byte
	digest         string
	manifestCalls  atomic.Int32
	tokenCalls     atomic.Int32
	wrongDigest    bool
	sawAccept      atomic.Value
	sawRepository  atomic.Value
	blobDigestSeen atomic.Value
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()

	up := &fakeUpstream{manifest: []byte(`{"schemaVersion":2,"mediaType":"` + indexType + `","manifests":[]}` + "\n")}
	sum := sha256.Sum256(up.manifest)
	up.digest = "sha256:" + hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, req *http.Request) {
		up.tokenCalls.Add(1)
		up.sawRepository.Store(req.URL.Query().Get("scope"))
		_ = json.NewEncoder(w).Encode(map[string]string{"token": testToken})
	})
	mux.HandleFunc("/v2/acme/solidping/manifests/", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		up.manifestCalls.Add(1)
		up.sawAccept.Store(req.Header.Get("Accept"))

		ref := strings.TrimPrefix(req.URL.Path, "/v2/acme/solidping/manifests/")
		if ref != "1.2.3" && ref != up.digest {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", indexType)

		if up.wrongDigest {
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
		} else {
			w.Header().Set("Docker-Content-Digest", up.digest)
		}

		_, _ = w.Write(up.manifest)
	})
	mux.HandleFunc("/v2/acme/solidping/blobs/", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		up.blobDigestSeen.Store(strings.TrimPrefix(req.URL.Path, "/v2/acme/solidping/blobs/"))
		http.Redirect(w, req, blobCDN, http.StatusTemporaryRedirect)
	})

	up.server = httptest.NewServer(mux)
	t.Cleanup(up.server.Close)

	return up
}

func newRegistry(upstream, serverVersion string) http.Handler {
	return agentregistry.New(agentregistry.Config{
		Upstream:           upstream,
		UpstreamRepository: "acme/solidping",
		Version:            func() string { return serverVersion },
	})
}

func do(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var payload struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Errors, 1)

	return payload.Errors[0].Code
}

func TestBaseEndpoint(t *testing.T) {
	t.Parallel()

	reg := newRegistry("http://127.0.0.1:1", "1.2.3")

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := do(t, reg, method, "/v2/")
		require.Equal(t, http.StatusOK, rec.Code, method)
		require.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-Api-Version"))
	}

	rec := do(t, reg, http.MethodPut, "/v2/solidping/manifests/current")
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestCurrentManifestIsByteForByte(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	up := newFakeUpstream(t)
	reg := newRegistry(up.server.URL, "v1.2.3")

	rec := do(t, reg, http.MethodGet, "/v2/solidping/manifests/current")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal(up.manifest, rec.Body.Bytes(), "the manifest is returned byte for byte")
	r.Equal(indexType, rec.Header().Get("Content-Type"))
	r.Equal(up.digest, rec.Header().Get("Docker-Content-Digest"))
	r.Contains(up.sawAccept.Load(), indexType)
	r.Equal("repository:acme/solidping:pull", up.sawRepository.Load())

	// HEAD answers the same headers, no body.
	head := do(t, reg, http.MethodHead, "/v2/solidping/manifests/current")
	r.Equal(http.StatusOK, head.Code)
	r.Equal(up.digest, head.Header().Get("Docker-Content-Digest"))
	r.Empty(head.Body.Bytes())

	// The tag resolution and the manifest are cached: one upstream fetch.
	r.Equal(int32(1), up.manifestCalls.Load())
	r.Equal(int32(1), up.tokenCalls.Load())
}

func TestExplicitTagAndDigest(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	up := newFakeUpstream(t)
	reg := newRegistry(up.server.URL, "9.9.9")

	byTag := do(t, reg, http.MethodGet, "/v2/solidping/manifests/1.2.3")
	r.Equal(http.StatusOK, byTag.Code)
	r.Equal(up.manifest, byTag.Body.Bytes())

	byDigest := do(t, reg, http.MethodGet, "/v2/solidping/manifests/"+up.digest)
	r.Equal(http.StatusOK, byDigest.Code)
	r.Equal(up.manifest, byDigest.Body.Bytes())
	r.Equal(up.digest, byDigest.Header().Get("Docker-Content-Digest"))

	// Unknown upstream tag: 404 in the registry shape.
	missing := do(t, reg, http.MethodGet, "/v2/solidping/manifests/0.0.1")
	r.Equal(http.StatusNotFound, missing.Code)
	r.Equal("MANIFEST_UNKNOWN", errorCode(t, missing))

	// A tag that is not a version never reaches the upstream.
	calls := up.manifestCalls.Load()
	junk := do(t, reg, http.MethodGet, "/v2/solidping/manifests/latest")
	r.Equal(http.StatusNotFound, junk.Code)
	r.Equal(calls, up.manifestCalls.Load())
}

func TestUnknownRepository(t *testing.T) {
	t.Parallel()

	reg := newRegistry("http://127.0.0.1:1", "1.2.3")

	for _, path := range []string{
		"/v2/other/manifests/current",
		"/v2/acme/solidping/manifests/current",
		"/v2/other/blobs/sha256:" + strings.Repeat("a", 64),
		"/v2/solidping",
	} {
		rec := do(t, reg, http.MethodGet, path)
		require.Equal(t, http.StatusNotFound, rec.Code, path)
		require.Equal(t, "NAME_UNKNOWN", errorCode(t, rec), path)
	}
}

func TestCurrentOnDevBuild(t *testing.T) {
	t.Parallel()

	up := newFakeUpstream(t)

	for _, v := range []string{"dev", ""} {
		rec := do(t, newRegistry(up.server.URL, v), http.MethodGet, "/v2/solidping/manifests/current")
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, "MANIFEST_UNKNOWN", errorCode(t, rec))
	}

	require.Equal(t, int32(0), up.manifestCalls.Load())
}

func TestBlobRedirectsToUpstreamLocation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	up := newFakeUpstream(t)
	reg := newRegistry(up.server.URL, "1.2.3")

	digest := "sha256:" + strings.Repeat("b", 64)
	rec := do(t, reg, http.MethodGet, "/v2/solidping/blobs/"+digest)
	r.Equal(http.StatusTemporaryRedirect, rec.Code)
	r.Equal(blobCDN, rec.Header().Get("Location"))
	r.Equal(digest, up.blobDigestSeen.Load())
	r.Empty(rec.Body.Bytes(), "layers never transit the server")

	bad := do(t, reg, http.MethodGet, "/v2/solidping/blobs/not-a-digest")
	r.Equal(http.StatusNotFound, bad.Code)
	r.Equal("BLOB_UNKNOWN", errorCode(t, bad))
}

func TestUpstreamDownIsBadGateway(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	down.Close() // nothing listens any more

	reg := newRegistry(down.URL, "1.2.3")

	for _, path := range []string{
		"/v2/solidping/manifests/current",
		"/v2/solidping/blobs/sha256:" + strings.Repeat("c", 64),
	} {
		rec := do(t, reg, http.MethodGet, path)
		r.Equal(http.StatusBadGateway, rec.Code, path)
		r.Equal("UNAVAILABLE", errorCode(t, rec))
	}

	// An upstream answering 5xx is a 502 too, never a 200 with its body.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/token" {
			_, _ = io.WriteString(w, `{"token":"t"}`)

			return
		}

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "oops")
	}))
	t.Cleanup(failing.Close)

	rec := do(t, newRegistry(failing.URL, "1.2.3"), http.MethodGet, "/v2/solidping/manifests/current")
	r.Equal(http.StatusBadGateway, rec.Code)
	r.NotContains(rec.Body.String(), "oops")
}

func TestUpstreamDigestMismatchIsBadGateway(t *testing.T) {
	t.Parallel()

	up := newFakeUpstream(t)
	up.wrongDigest = true

	rec := do(t, newRegistry(up.server.URL, "1.2.3"), http.MethodGet, "/v2/solidping/manifests/current")
	require.Equal(t, http.StatusBadGateway, rec.Code)
}
