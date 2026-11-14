package checkjs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// refererSecret needs percent-encoding in a query string, so the encoded form
// in Referer is not a raw match for the secret.
const refererSecret = "s3cr3t token/+="

// runBothPaths runs script through the isolated child path (the default
// Execute path, where http.* runs in the host) and in-process.
func runBothPaths(t *testing.T, script string, env, secrets map[string]string) map[string]*checkerdef.Result {
	t.Helper()

	cfg := &JSConfig{Script: script, Timeout: 10 * time.Second, Env: env, Secrets: secrets}

	isolated, err := (&JSChecker{}).Execute(context.Background(), cfg)
	require.NoError(t, err)

	inProcess := executeInProcess(context.Background(), cfg, cfg.Timeout, nil)

	return map[string]*checkerdef.Result{"isolated": isolated, "in-process": inProcess}
}

// refererScript sends the percent-encoded secret in the URL query.
const refererScript = `
var resp = http.get(env.URL + "?api_key=" + encodeURIComponent(secrets.TOKEN));
if (resp.error) { return {status: "error", output: {error: resp.error}}; }
return {status: resp.statusCode === 200 ? "up" : "down"};
`

// TestRedirectRefererCrossOrigin: a cross-origin hop never carries Referer,
// which would hold the resolved (percent-encoded) secret of the query string;
// a same-origin hop keeps it (spec 2026-11-14-02, AM2).
func TestRedirectRefererCrossOrigin(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		reached int
		referer []string
	)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached++
		referer = append(referer, r.Header.Get("Referer"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)

	start := redirectingServer(t, other.URL+"/landing")

	results := runBothPaths(t, refererScript,
		map[string]string{"URL": start.URL + "/go"}, map[string]string{"TOKEN": refererSecret})

	for name, result := range results {
		require.Equal(t, "up", result.Status.String(), "%s output: %#v", name, result.Output)
		require.NotContains(t, result.Output, OutputKeyRedirectDroppedSecretHeaders, name)
	}

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, 2, reached)
	require.Equal(t, []string{"", ""}, referer)
}

func TestRedirectRefererSameOrigin(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		referer []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" {
			http.Redirect(w, r, "/landing", http.StatusFound)

			return
		}

		mu.Lock()
		referer = append(referer, r.Header.Get("Referer"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	results := runBothPaths(t, refererScript,
		map[string]string{"URL": server.URL + "/go"}, map[string]string{"TOKEN": refererSecret})

	for name, result := range results {
		require.Equal(t, "up", result.Status.String(), "%s output: %#v", name, result.Output)
	}

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, referer, 2)

	for _, value := range referer {
		require.Contains(t, value, "api_key=s3cr3t%20token%2F%2B%3D")
	}
}

// bodyScript POSTs the secret as the body and reports the response.
const bodyScript = `
var resp = http.post(env.URL, {body: "token=" + secrets.TOKEN});
if (resp.error) { return {status: "error", output: {error: resp.error}}; }
return {status: "up", output: {code: resp.statusCode, withheld: resp.redirectBodyWithheld === true}};
`

// TestRedirectBodyCrossOrigin: a cross-origin 307/308 would replay the body,
// so it is not followed: the script gets the redirect response, flagged, and
// the check's output reports redirect_body_withheld (spec 2026-11-14-02, AM3).
func TestRedirectBodyCrossOrigin(t *testing.T) {
	t.Parallel()

	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()

			var hits atomic.Int32

			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(other.Close)

			start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+"/landing", code)
			}))
			t.Cleanup(start.Close)

			results := runBothPaths(t, bodyScript,
				map[string]string{"URL": start.URL + "/submit"}, map[string]string{"TOKEN": redirectTestSecret})

			for name, result := range results {
				require.Equal(t, "up", result.Status.String(), "%s output: %#v", name, result.Output)
				require.EqualValues(t, code, result.Output["code"], name)
				require.Equal(t, true, result.Output["withheld"], name)
				require.Equal(t, true, result.Output[OutputKeyRedirectBodyWithheld], name)
			}

			require.Equal(t, int32(0), hits.Load(), "the other origin must receive no request")
		})
	}
}

func TestRedirectBodySameOrigin(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		bodies []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			http.Redirect(w, r, "/next", http.StatusTemporaryRedirect)

			return
		}

		data, _ := io.ReadAll(r.Body)

		mu.Lock()
		bodies = append(bodies, string(data))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	results := runBothPaths(t, bodyScript,
		map[string]string{"URL": server.URL + "/submit"}, map[string]string{"TOKEN": redirectTestSecret})

	for name, result := range results {
		require.Equal(t, "up", result.Status.String(), "%s output: %#v", name, result.Output)
		require.EqualValues(t, http.StatusOK, result.Output["code"], name)
		require.Equal(t, false, result.Output["withheld"], name)
		require.NotContains(t, result.Output, OutputKeyRedirectBodyWithheld, name)
	}

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []string{"token=" + redirectTestSecret, "token=" + redirectTestSecret}, bodies)
}
