package freebox_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

func TestValidateBaseURLAcceptsDefaultAndEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.NoError(freebox.ValidateBaseURL(""))
	r.NoError(freebox.ValidateBaseURL("  "))
	r.NoError(freebox.ValidateBaseURL(freebox.DefaultBaseURL))
}

func TestValidateBaseURLAcceptsSelfHostedShapes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, raw := range []string{
		"http://mafreebox.freebox.fr",
		"http://mafreebox.freebox.fr/",
		"http://192.168.1.254",
		"http://10.0.0.1:80",
		"https://192.168.1.254:8443",
		"https://192.168.1.254:443",
		"https://some-serial.freebox.fr",
		"https://some-serial.freebox.fr:8443",
		"https://203.0.113.10:8443", // public IP, https, remote access
		"https://203.0.113.10:443",
		// A private-range IP is the member's own LAN: any port is accepted —
		// a home Freebox can be reconfigured onto a nonstandard port, and
		// this is also how test fixtures point at a fake local Freebox
		// (httptest.Server binds 127.0.0.1 on a random port).
		"http://192.168.1.254:2222",
		"http://127.0.0.1:60283",
		"https://127.0.0.1:60283",
	} {
		r.NoError(freebox.ValidateBaseURL(raw), raw)
	}
}

func TestValidateBaseURLRejectsBadShapes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cases := []struct {
		name string
		raw  string
	}{
		{"userinfo", "http://user:pass@mafreebox.freebox.fr"},
		{"weird port on freebox hostname", "http://mafreebox.freebox.fr:22"},
		{"weird port on public IP", "https://203.0.113.10:2222"},
		{"non-freebox non-IP host", "https://evil.example"},
		{"non-freebox non-IP host http", "http://evil.example"},
		{"public IP over http", "http://203.0.113.10"},
		{"bad scheme", "ftp://192.168.1.254"},
		{"no scheme", "mafreebox.freebox.fr"},
		{"missing host", "http:///"},
		{"unparsable", "http://%zz"},
	}

	for _, tc := range cases {
		err := freebox.ValidateBaseURL(tc.raw)
		r.Error(err, tc.name)
		r.ErrorIs(err, freebox.ErrBaseURLInvalid, tc.name)
	}
}
