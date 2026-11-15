package checkprometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestNewClient_HTTPSToHTTPRedirectDropsAuthorization: net/http keeps
// Authorization on a same-hostname https -> http hop; the check's client does
// not (spec 2026-11-15-02). The client's transport is swapped for one that
// trusts the test TLS server, the CheckRedirect under test is newClient's.
func TestNewClient_HTTPSToHTTPRedirectDropsAuthorization(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var (
		mu     sync.Mutex
		header http.Header
	)

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		header = req.Header.Clone()
		mu.Unlock()

		_, _ = w.Write([]byte("up 1\n"))
	}))
	t.Cleanup(plain.Close)

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, plain.URL+"/metrics", http.StatusFound)
	}))
	t.Cleanup(secure.Close)

	// Both servers listen on 127.0.0.1: the hop keeps the hostname.
	r.True(strings.HasPrefix(secure.URL, "https://127.0.0.1:"))
	r.True(strings.HasPrefix(plain.URL, "http://127.0.0.1:"))

	cfg := &PrometheusConfig{URL: secure.URL, Headers: map[string]string{"Authorization": "Bearer t"}}
	policy := checkerdef.NewCredentialRedirectPolicy(checkerdef.HeaderNames(cfg.Headers))

	client := newClient(t.Context(), cfg, policy)
	client.Transport = secure.Client().Transport

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, secure.URL+"/metrics", nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer t")

	resp, err := client.Do(req)
	r.NoError(err)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	mu.Lock()
	defer mu.Unlock()

	r.NotNil(header)
	r.Empty(header.Get("Authorization"))
	r.Equal([]string{"Authorization"}, policy.Dropped())
}
