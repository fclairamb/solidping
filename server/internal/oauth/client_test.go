package oauth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsLoopbackRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{"ipv4 loopback any port", "http://127.0.0.1:5173/callback", true},
		{"localhost", "http://localhost:1410/cb", true},
		{"ipv6 loopback", "http://[::1]:8080/cb", true},
		{"https loopback is not the loopback http exception", "https://127.0.0.1/cb", false},
		{"non-loopback http", "http://example.com/cb", false},
		{"public https", "https://app.example.com/cb", false},
		{"garbage", "::::", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, IsLoopbackRedirectURI(tc.uri))
		})
	}
}

func TestIsValidRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{"https web", "https://app.example.com/callback", true},
		{"loopback http", "http://127.0.0.1:9000/cb", true},
		{"plain http non-loopback rejected", "http://evil.example.com/cb", false},
		{"fragment rejected", "https://app.example.com/cb#frag", false},
		{"no host rejected", "https:///cb", false},
		{"empty rejected", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, IsValidRedirectURI(tc.uri))
		})
	}
}

func TestRedirectURIAllowed(t *testing.T) {
	t.Parallel()

	registered := []string{
		"https://app.example.com/callback",
		"http://127.0.0.1:1234/cb",
	}

	tests := []struct {
		name      string
		requested string
		want      bool
	}{
		{"exact https match", "https://app.example.com/callback", true},
		{"https path mismatch rejected", "https://app.example.com/other", false},
		{"https host mismatch rejected", "https://evil.example.com/callback", false},
		{"loopback different port accepted", "http://127.0.0.1:55555/cb", true},
		{"loopback different path rejected", "http://127.0.0.1:55555/evil", false},
		{"empty rejected", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, RedirectURIAllowed(tc.requested, registered))
		})
	}
}

func TestScopesValid(t *testing.T) {
	t.Parallel()

	require.True(t, ScopesValid([]string{"mcp"}))
	require.True(t, ScopesValid([]string{"mcp:read"}))
	require.True(t, ScopesValid([]string{"mcp", "mcp:read"}))
	require.True(t, ScopesValid(nil))
	require.False(t, ScopesValid([]string{"admin"}))
	require.False(t, ScopesValid([]string{"mcp", "write"}))
}
