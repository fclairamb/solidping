package securityheaders

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateEmbedOriginAccepts(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"https://intranet.acme.com":       "https://intranet.acme.com",
		"  HTTPS://Intranet.Acme.com/  ":  "https://intranet.acme.com",
		"http://localhost:8080":           "http://localhost:8080",
		"https://*.acme.com":              "https://*.acme.com",
		"https://*.eu.acme.com:8443":      "https://*.eu.acme.com:8443",
		"https://10.0.0.12":               "https://10.0.0.12",
		"https://status-embed.acme-co.io": "https://status-embed.acme-co.io",
	}

	for input, want := range tests {
		got, err := ValidateEmbedOrigin(input)
		require.NoErrorf(t, err, "%q", input)
		require.Equal(t, want, got)
	}
}

func TestValidateEmbedOriginRejects(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"",
		"*",
		"https://*",
		"https://*.com",          // wildcard over a TLD
		"https://a.*.acme.com",   // wildcard not in first label
		"acme.com",               // no scheme
		"ftp://acme.com",         // not http(s)
		"https://acme.com/embed", // path
		"https://acme.com?x=1",   // query
		"https://acme.com#x",     // fragment
		"https://user@acme.com",  // userinfo
		"https://acme.com; script-src *",
		"https://acme.com https://evil.example",
		"https://acme.com,https://evil.example",
		"'self'",
		"'unsafe-inline'",
		"https://acme .com",
		"javascript:alert(1)",
		"data:",
	} {
		_, err := ValidateEmbedOrigin(input)
		require.ErrorIsf(t, err, ErrInvalidEmbedOrigin, "%q must be refused", input)
	}
}

func TestNormalizeEmbedOrigins(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got, err := NormalizeEmbedOrigins([]string{"https://a.acme.com", "", "HTTPS://A.acme.com/", "https://b.acme.com"})
	r.NoError(err)
	r.Equal([]string{"https://a.acme.com", "https://b.acme.com"}, got)

	_, err = NormalizeEmbedOrigins([]string{"https://a.acme.com", "https://acme.com/path"})
	r.ErrorIs(err, ErrInvalidEmbedOrigin)

	tooMany := make([]string, 0, MaxEmbedOrigins+1)
	for i := 0; i <= MaxEmbedOrigins; i++ {
		tooMany = append(tooMany, "https://s"+strings.Repeat("a", i+1)+".acme.com")
	}

	_, err = NormalizeEmbedOrigins(tooMany)
	r.ErrorIs(err, ErrTooManyEmbedOrigins)
}

func TestParseEmbedOriginsIsLenientOnRead(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A row written behind the API's back (or by an older build) must never
	// be able to inject a directive — the bad entries are dropped, the good
	// ones still apply.
	got := ParseEmbedOrigins("https://a.acme.com, https://acme.com;script-src *,\nhttps://b.acme.com https://a.acme.com")
	r.Equal([]string{"https://a.acme.com", "https://b.acme.com"}, got)

	r.Empty(ParseEmbedOrigins(""))
	r.Equal("https://a.acme.com,https://b.acme.com", FormatEmbedOrigins(got))
}
