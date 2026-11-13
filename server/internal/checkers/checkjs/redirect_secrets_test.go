package checkjs

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const redirectTestSecret = "s3cr3t-token"

// headerRecorder is an httptest server that records the headers of the last
// request it received.
type headerRecorder struct {
	mu     sync.Mutex
	header http.Header
}

func (h *headerRecorder) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.header = r.Header.Clone()
}

func (h *headerRecorder) last() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.header
}

// redirectScript sends one base64-encoded and one hex-encoded secret header,
// plus generic ones, to url, and returns the redirect info in its output.
const redirectScript = `
var tok = secrets.TOKEN || env.TOKEN;
var hex = "";
for (var i = 0; i < tok.length; i++) { hex += tok.charCodeAt(i).toString(16); }
var resp = http.get(env.URL, {headers: {
  "X-B64-Token": base64.encode(tok),
  "X-Hex-Token": hex,
  "Accept": "application/json",
  "User-Agent": "acme-probe/1.0"
}});
if (resp.error) { return {status: "error", output: {error: resp.error}}; }
return {status: resp.statusCode === 200 ? "up" : "down",
  output: {dropped: resp.redirectDroppedHeaders || []}};
`

func redirectFixture(t *testing.T) (*headerRecorder, *httptest.Server) {
	t.Helper()

	rec := &headerRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return rec, server
}

// redirectingServer answers every request with a 302 to target.
func redirectingServer(t *testing.T, target string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	return server
}

// otherHostURL rewrites an httptest URL from 127.0.0.1 to localhost, so the
// redirect crosses hosts.
func otherHostURL(raw string) string {
	return strings.Replace(raw, "127.0.0.1", "localhost", 1)
}

func TestRedirectStripsEncodedSecretHeadersCrossHost(t *testing.T) {
	t.Parallel()

	rec, final := redirectFixture(t)
	start := redirectingServer(t, otherHostURL(final.URL)+"/landing")

	result := runDocExample(t, redirectScript,
		map[string]string{"URL": start.URL + "/go"},
		map[string]string{"TOKEN": redirectTestSecret})

	require.Equal(t, "up", result.Status.String(), "output: %#v", result.Output)

	got := rec.last()
	require.NotNil(t, got)
	require.Empty(t, got.Get("X-B64-Token"), "base64-encoded secret must not cross hosts")
	require.Empty(t, got.Get("X-Hex-Token"), "hex-encoded secret must not cross hosts")
	require.Equal(t, "application/json", got.Get("Accept"))
	require.Equal(t, "acme-probe/1.0", got.Get("User-Agent"))

	want := []string{"X-B64-Token", "X-Hex-Token"}
	require.Equal(t, want, result.Output[OutputKeyRedirectDroppedSecretHeaders], "%#v", result.Output)
	require.ElementsMatch(t, want, result.Output["dropped"])

	// Names only, never the values.
	for _, value := range result.Output {
		text, _ := value.(string)
		require.NotContains(t, text, base64.StdEncoding.EncodeToString([]byte(redirectTestSecret)))
		require.NotContains(t, text, hex.EncodeToString([]byte(redirectTestSecret)))
	}
}

func TestRedirectKeepsEncodedSecretHeadersSameHost(t *testing.T) {
	t.Parallel()

	rec := &headerRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" {
			http.Redirect(w, r, "/landing", http.StatusFound)

			return
		}

		rec.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	result := runDocExample(t, redirectScript,
		map[string]string{"URL": server.URL + "/go"},
		map[string]string{"TOKEN": redirectTestSecret})

	require.Equal(t, "up", result.Status.String(), "output: %#v", result.Output)

	got := rec.last()
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(redirectTestSecret)), got.Get("X-B64-Token"))
	require.Equal(t, hex.EncodeToString([]byte(redirectTestSecret)), got.Get("X-Hex-Token"))
	require.NotContains(t, result.Output, OutputKeyRedirectDroppedSecretHeaders)
}

func TestRedirectWithoutSecretsKeepsCustomHeadersCrossHost(t *testing.T) {
	t.Parallel()

	rec, final := redirectFixture(t)
	start := redirectingServer(t, otherHostURL(final.URL)+"/landing")

	result := runDocExample(t, redirectScript,
		map[string]string{"URL": start.URL + "/go", "TOKEN": redirectTestSecret},
		nil)

	require.Equal(t, "up", result.Status.String(), "output: %#v", result.Output)

	got := rec.last()
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(redirectTestSecret)), got.Get("X-B64-Token"))
	require.Equal(t, hex.EncodeToString([]byte(redirectTestSecret)), got.Get("X-Hex-Token"))
	require.NotContains(t, result.Output, OutputKeyRedirectDroppedSecretHeaders)
}
