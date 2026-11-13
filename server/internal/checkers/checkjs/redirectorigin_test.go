package checkjs

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRedirectPolicyStripsSecretsOnSchemeDowngrade covers the JS copy of the
// redirect origin rule (spec 2026-11-13-04): a header built from a secret is
// dropped from a same-host https -> http hop, kept on a same-scheme hop and on
// an http -> https upgrade.
func TestRedirectPolicyStripsSecretsOnSchemeDowngrade(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		first    string
		next     string
		wantKept bool
	}{
		{"port-less downgrade", "https://a.acme.com/", "http://a.acme.com/next", false},
		{"explicit-port downgrade", "https://a.acme.com:8443/", "http://a.acme.com:8443/next", false},
		{"cross host", "https://a.acme.com/", "https://b.acme.com/next", false},
		{"same-scheme same-host", "https://a.acme.com/", "https://a.acme.com/next", true},
		{"upgrade", "http://a.acme.com/", "https://a.acme.com/next", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			first, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.first, nil)
			require.NoError(t, err)

			next, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.next, nil)
			require.NoError(t, err)

			next.Header.Set("X-Api-Key", "Bearer tok-123")
			next.Header.Set("X-Trace", "1")

			var redirects []map[string]any

			policy := redirectPolicy(
				&httpOptions{followRedirects: true, maxRedirects: 5},
				&redirects,
				map[string]string{"apiKey": "tok-123"},
			)
			require.NoError(t, policy(next, []*http.Request{first}))

			require.Equal(t, "1", next.Header.Get("X-Trace"))

			if tc.wantKept {
				require.Equal(t, "Bearer tok-123", next.Header.Get("X-Api-Key"))
			} else {
				require.Empty(t, next.Header.Get("X-Api-Key"))
			}
		})
	}
}
