package checkerdef

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// redirectHop builds the request net/http would hand CheckRedirect for a hop
// from first to next: the original headers copied over, plus a Referer.
func redirectHop(t *testing.T, first, next string, header http.Header) (*http.Request, []*http.Request) {
	t.Helper()

	orig, err := http.NewRequest(http.MethodGet, first, nil)
	require.NoError(t, err)

	orig.Header = header.Clone()

	req, err := http.NewRequest(http.MethodGet, next, nil)
	require.NoError(t, err)

	req.Header = header.Clone()
	req.Header.Set("Referer", first)

	return req, []*http.Request{orig}
}

func policyHeaders() http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer t")
	header.Set("Cookie", "session=s")
	header.Set("X-Api-Key", "k")
	header.Set("User-Agent", "solidping")

	return header
}

func TestCredentialRedirectPolicy_CrossOriginDropsNamedHeaders(t *testing.T) {
	t.Parallel()

	for _, next := range []string{"https://b.acme.com/", "http://a.acme.com/", "https://a.acme.com:8443/"} {
		t.Run(next, func(t *testing.T) {
			t.Parallel()

			policy := NewCredentialRedirectPolicy([]string{"x-api-key"})
			req, via := redirectHop(t, "https://a.acme.com/metrics?token=q", next, policyHeaders())

			require.NoError(t, policy.CheckRedirect(req, via))
			require.Empty(t, req.Header.Get("Authorization"))
			require.Empty(t, req.Header.Get("Cookie"))
			require.Empty(t, req.Header.Get("X-Api-Key"))
			require.Empty(t, req.Header.Get("Referer"))
			require.Equal(t, "solidping", req.Header.Get("User-Agent"))
			require.Equal(t, []string{"Authorization", "Cookie", "X-Api-Key"}, policy.Dropped())

			output := map[string]any{}
			policy.ReportDropped(output)
			require.Equal(t, []string{"Authorization", "Cookie", "X-Api-Key"},
				output[OutputKeyRedirectDroppedSecretHeaders])
		})
	}
}

func TestCredentialRedirectPolicy_ReportsHeadersNetHTTPAlreadyStripped(t *testing.T) {
	t.Parallel()

	policy := NewCredentialRedirectPolicy(nil)
	req, via := redirectHop(t, "https://a.acme.com/", "https://b.acme.com/", policyHeaders())
	req.Header.Del("Authorization") // what net/http does on a host change

	require.NoError(t, policy.CheckRedirect(req, via))
	require.Contains(t, policy.Dropped(), "Authorization")
}

func TestCredentialRedirectPolicy_SameOriginKeepsHeaders(t *testing.T) {
	t.Parallel()

	for _, next := range []string{"https://a.acme.com/other", "https://a.acme.com:443/x"} {
		policy := NewCredentialRedirectPolicy([]string{"X-Api-Key"})
		req, via := redirectHop(t, "https://a.acme.com/metrics", next, policyHeaders())

		require.NoError(t, policy.CheckRedirect(req, via))
		require.Equal(t, "Bearer t", req.Header.Get("Authorization"))
		require.Equal(t, "k", req.Header.Get("X-Api-Key"))
		require.NotEmpty(t, req.Header.Get("Referer"))
		require.Empty(t, policy.Dropped())

		output := map[string]any{}
		policy.ReportDropped(output)
		require.NotContains(t, output, OutputKeyRedirectDroppedSecretHeaders)
	}

	// An http -> https upgrade on the same host is same-origin.
	policy := NewCredentialRedirectPolicy(nil)
	req, via := redirectHop(t, "http://a.acme.com/", "https://a.acme.com/", policyHeaders())

	require.NoError(t, policy.CheckRedirect(req, via))
	require.Equal(t, "Bearer t", req.Header.Get("Authorization"))
}

func TestCredentialRedirectPolicy_BodyWithheld(t *testing.T) {
	t.Parallel()

	policy := NewCredentialRedirectPolicy(nil)
	req, via := redirectHop(t, "https://a.acme.com/", "https://b.acme.com/", policyHeaders())
	req.Body = http.NoBody
	req.ContentLength = 4

	require.ErrorIs(t, policy.CheckRedirect(req, via), http.ErrUseLastResponse)
	require.True(t, policy.BodyWithheld())

	// The same body on a same-origin hop is followed.
	same := NewCredentialRedirectPolicy(nil)
	req, via = redirectHop(t, "https://a.acme.com/", "https://a.acme.com/next", policyHeaders())
	req.ContentLength = 4

	require.NoError(t, same.CheckRedirect(req, via))
	require.False(t, same.BodyWithheld())
}

func TestCredentialRedirectPolicy_ChainCap(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	policy := NewCredentialRedirectPolicy(nil)
	policy.MaxVia = 3
	client := &http.Client{CheckRedirect: policy.CheckRedirect}

	resp, err := client.Get(srv.URL) //nolint:noctx // test
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.ErrorIs(t, err, ErrTooManyRedirects)
	require.True(t, strings.Contains(err.Error(), "stopped after 3 redirects"))
	require.EqualValues(t, 3, hits.Load())

	// The default cap is DefaultMaxRedirects.
	hits.Store(0)
	client = &http.Client{CheckRedirect: NewCredentialRedirectPolicy(nil).CheckRedirect}

	resp, err = client.Get(srv.URL) //nolint:noctx // test
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.ErrorIs(t, err, ErrTooManyRedirects)
	require.EqualValues(t, DefaultMaxRedirects, hits.Load())
}
