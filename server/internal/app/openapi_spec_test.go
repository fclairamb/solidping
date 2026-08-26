package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The real embedded document, so these tests break if its `servers:` block is
// ever reshaped in a way the rewriter does not understand.
func embeddedSpec(t *testing.T) []byte {
	t.Helper()

	raw, err := openAPIFiles.ReadFile("openapi/openapi.yaml")
	require.NoError(t, err)

	return raw
}

func requestTo(host string, headers map[string]string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/openapi.yaml", nil)
	req.Host = host

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return req
}

func TestOpenAPIServersUsesTheRequestOrigin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	out := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo("solidping.k8xp.com", map[string]string{
		"X-Forwarded-Proto": "https",
	})))

	r.Contains(out, "  - url: https://solidping.k8xp.com")
	r.Contains(out, "    description: This server")
	// The static localhost entry is the thing being replaced — if it survives,
	// a generated client can still be pointed at the wrong host.
	r.NotContains(out, "http://localhost:4000")
}

func TestOpenAPIServersKeepsTheCloudButNeverTwice(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Self-hosted: the cloud stays listed as a second entry.
	elsewhere := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo("monitoring.acme.example", map[string]string{
		"X-Forwarded-Proto": "https",
	})))
	r.Contains(elsewhere, "  - url: https://monitoring.acme.example")
	r.Equal(1, strings.Count(elsewhere, "url: https://solidping.io"))

	// On the cloud itself the origin IS the cloud, so it must appear once, not
	// once as "This server" and again as "SolidPing cloud".
	onCloud := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo("solidping.io", map[string]string{
		"X-Forwarded-Proto": "https",
	})))
	r.Equal(1, strings.Count(onCloud, "url: https://solidping.io"))
	r.Contains(onCloud, "    description: This server")
	r.NotContains(onCloud, "    description: SolidPing cloud")
}

func TestOpenAPIServersHonoursSchemeAndPort(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// No proxy header, no TLS: plain http, and the port is part of the origin.
	local := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo("localhost:4000", nil)))
	r.Contains(local, "  - url: http://localhost:4000")

	// A proxy may send a comma-joined list; the first token wins (requestScheme).
	chained := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo("acme.example", map[string]string{
		"X-Forwarded-Proto": "https, http",
	})))
	r.Contains(chained, "  - url: https://acme.example")
}

// The rewriter edits ONE block. This is the guard that it does not disturb the
// rest of a document three other test files parse and the docs site builds from.
func TestOpenAPIRewriteLeavesTheRestOfTheDocumentIntact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	original := string(embeddedSpec(t))
	out := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo("acme.example", map[string]string{
		"X-Forwarded-Proto": "https",
	})))

	// Everything before `servers:` and from `tags:` onwards is byte-identical.
	head, _, found := strings.Cut(original, "\n"+openAPIServersKey+"\n")
	r.True(found)
	r.True(strings.HasPrefix(out, head), "the document head must be untouched")

	_, tailOriginal, found := strings.Cut(original, "\ntags:\n")
	r.True(found)
	_, tailRewritten, found := strings.Cut(out, "\ntags:\n")
	r.True(found)
	r.Equal(tailOriginal, tailRewritten, "everything from tags: onwards must be untouched")

	// The blank line separating the block from `tags:` is preserved, so the
	// rewrite cannot slowly reflow the document.
	r.Contains(out, "\n\ntags:\n")
}

// The Host header is client-controlled. A value that could not appear in a real
// deployment must never reach the document — falling back to the embedded list
// is always safe, since that is what callers got before this existed.
func TestOpenAPIServersFallsBackOnAnUntrustworthyHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	original := string(embeddedSpec(t))

	for name, host := range map[string]string{
		"empty":             "",
		"newline":           "acme.example\n  - url: https://evil.example",
		"space":             "acme example",
		"quote":             "acme.example\"",
		"yaml comment":      "acme.example #",
		"path traversal":    "acme.example/../../etc",
		"control character": "acme.example\x00",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo(host, nil)))
			r.Equal(original, out, "an unsafe host must leave the spec untouched")
			r.NotContains(out, "evil.example")
		})
	}
}

// Positive control for the fallback above: without it, a rewriter that always
// bailed out would pass every fallback case while doing nothing useful.
func TestOpenAPIServersActuallyRewritesForAnOrdinaryHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	original := string(embeddedSpec(t))
	out := string(rewriteOpenAPIServers(embeddedSpec(t), requestTo("acme.example", nil)))

	r.NotEqual(original, out, "an ordinary host must produce a rewritten document")
	r.Contains(out, "  - url: http://acme.example")
}

func TestServeOpenAPISpecSetsVaryAndServesTheRewrittenBody(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := &Server{}
	recorder := httptest.NewRecorder()
	req := requestTo("solidping.k8xp.com", map[string]string{"X-Forwarded-Proto": "https"})

	r.NoError(server.serveOpenAPISpec(openAPIFiles, "openapi/openapi.yaml")(recorder, req))

	r.Equal(http.StatusOK, recorder.Code)
	// A shared cache must not hand an http:// spec to an https:// client.
	r.Equal("X-Forwarded-Proto", recorder.Header().Get("Vary"))
	// mime.TypeByExtension takes an extension, not a path, so the shared
	// serveFile helper sends this document untyped. Pin the real type.
	r.Equal("application/yaml; charset=utf-8", recorder.Header().Get("Content-Type"))
	r.Contains(recorder.Body.String(), "  - url: https://solidping.k8xp.com")
}
