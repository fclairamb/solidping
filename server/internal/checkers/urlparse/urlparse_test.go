package urlparse

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		url        string
		wantType   checkerdef.CheckType
		wantHost   string
		wantPort   int
		wantTLS    bool
		wantRecord string
		wantDomain string
		wantErr    bool
	}{
		// HTTP
		{
			"http basic", "http://example.com",
			checkerdef.CheckTypeHTTP, "example.com", 0, false, "", "", false,
		},
		{
			"https basic", "https://example.com",
			checkerdef.CheckTypeHTTP, "example.com", 0, true, "", "", false,
		},
		{
			"https with port", "https://example.com:8443",
			checkerdef.CheckTypeHTTP, "example.com", 8443, true, "", "", false,
		},
		{
			"https with path", "https://example.com/api/v1",
			checkerdef.CheckTypeHTTP, "example.com", 0, true, "", "", false,
		},

		// TCP
		{
			"tcp basic", "tcp://example.com:3306",
			checkerdef.CheckTypeTCP, "example.com", 3306, false, "", "", false,
		},
		{
			"tcps basic", "tcps://example.com:443",
			checkerdef.CheckTypeTCP, "example.com", 443, true, "", "", false,
		},
		{"tcp no port", "tcp://example.com", "", "", 0, false, "", "", true},

		// ICMP (ping)
		{
			"ping basic", "ping://8.8.8.8",
			checkerdef.CheckTypeICMP, "8.8.8.8", 0, false, "", "", false,
		},
		{
			"icmp basic", "icmp://google.com",
			checkerdef.CheckTypeICMP, "google.com", 0, false, "", "", false,
		},

		// DNS - new format: dns://resolver/domain?type=X
		{
			"dns with resolver", "dns://8.8.8.8/example.com",
			checkerdef.CheckTypeDNS, "8.8.8.8", 0, false, "A", "example.com", false,
		},
		{
			"dns with type", "dns://8.8.8.8/example.com?type=MX",
			checkerdef.CheckTypeDNS, "8.8.8.8", 0, false, "MX", "example.com", false,
		},
		{
			"dns with port", "dns://8.8.8.8:53/example.com?type=AAAA",
			checkerdef.CheckTypeDNS, "8.8.8.8", 53, false, "AAAA", "example.com", false,
		},
		{
			"dns system resolver", "dns:///example.com",
			checkerdef.CheckTypeDNS, "", 0, false, "A", "example.com", false,
		},
		{
			"dns system resolver MX", "dns:///example.com?type=MX",
			checkerdef.CheckTypeDNS, "", 0, false, "MX", "example.com", false,
		},
		{
			"dns cloudflare", "dns://1.1.1.1/google.com?type=TXT",
			checkerdef.CheckTypeDNS, "1.1.1.1", 0, false, "TXT", "google.com", false,
		},
		{
			"dns hostname resolver", "dns://dns.google/example.com",
			checkerdef.CheckTypeDNS, "dns.google", 0, false, "A", "example.com", false,
		},

		// Errors
		{"empty", "", "", "", 0, false, "", "", true},
		{"invalid scheme", "ftp://example.com", "", "", 0, false, "", "", true},
		{"no host http", "https://", "", "", 0, false, "", "", true},
		{"dns no domain", "dns://8.8.8.8/", "", "", 0, false, "", "", true},
		{
			"dns invalid type", "dns://8.8.8.8/example.com?type=INVALID",
			"", "", 0, false, "", "", true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			parsed, err := Parse(tt.url)

			if tt.wantErr {
				r.Error(err)
				return
			}

			r.NoError(err)
			r.Equal(tt.wantType, parsed.CheckType)
			r.Equal(tt.wantHost, parsed.Host)
			r.Equal(tt.wantPort, parsed.Port)
			r.Equal(tt.wantTLS, parsed.TLS)
			r.Equal(tt.wantRecord, parsed.RecordType)
			r.Equal(tt.wantDomain, parsed.DNSDomain)
		})
	}
}

func TestSuggestNameSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url      string
		wantName string
		wantSlug string
	}{
		{"https://google.com", "google.com (http)", "google-com"},
		{"tcp://db.example.com:3306", "db.example.com (tcp)", "db-example-com-3306"},
		{"tcps://db.example.com:443", "db.example.com (tcp)", "db-example-com"},
		{"ping://8.8.8.8", "8.8.8.8 (icmp)", "8-8-8-8"},
		{"icmp://1.1.1.1", "1.1.1.1 (icmp)", "1-1-1-1"},
		// DNS uses domain (not resolver) for name/slug
		{"dns://8.8.8.8/google.com", "google.com (dns)", "google-com"},
		{"dns://1.1.1.1/example.com?type=MX", "example.com (dns)", "example-com"},
		{"dns:///example.com", "example.com (dns)", "example-com"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			parsed, err := Parse(tt.url)
			r.NoError(err)

			name, slug := parsed.SuggestNameSlug()
			r.Equal(tt.wantName, name)
			r.Equal(tt.wantSlug, slug)
		})
	}
}

func TestResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url          string
		wantResolver string
	}{
		{"dns://8.8.8.8/example.com", "8.8.8.8"},
		{"dns://8.8.8.8:53/example.com", "8.8.8.8"},
		{"dns://8.8.8.8:5353/example.com", "8.8.8.8:5353"},
		{"dns:///example.com", ""},
		{"dns://dns.google/example.com", "dns.google"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			parsed, err := Parse(tt.url)
			r.NoError(err)
			r.Equal(tt.wantResolver, parsed.Resolver())
		})
	}
}

func TestInferCheckType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url      string
		wantType checkerdef.CheckType
	}{
		{"https://google.com", checkerdef.CheckTypeHTTP},
		{"http://example.com", checkerdef.CheckTypeHTTP},
		{"tcp://example.com:3306", checkerdef.CheckTypeTCP},
		{"tcps://example.com:443", checkerdef.CheckTypeTCP},
		{"ping://8.8.8.8", checkerdef.CheckTypeICMP},
		{"icmp://google.com", checkerdef.CheckTypeICMP},
		{"dns://8.8.8.8/example.com", checkerdef.CheckTypeDNS},
		{"dns:///example.com", checkerdef.CheckTypeDNS},
		// Invalid URLs return empty string
		{"", ""},
		{"ftp://example.com", ""},
		{"invalid-url", ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tt.wantType, InferCheckType(tt.url))
		})
	}
}
