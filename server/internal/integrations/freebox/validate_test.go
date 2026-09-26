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
		"http://10.0.0.1:8443",
		"https://192.168.1.254:8443",
		"https://192.168.1.254:443",
		"https://172.16.0.1:443",
		"https://[fc00::1]:443",
		"https://some-serial.freebox.fr",
		"https://some-serial.freebox.fr:8443",
		"https://203.0.113.10:8443", // public IP, https, remote access
		"https://203.0.113.10:443",
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
		{"weird port on private IP", "http://192.168.1.254:6379"},
		{"non-freebox non-IP host", "https://evil.example"},
		{"non-freebox non-IP host http", "http://evil.example"},
		{"public IP over http", "http://203.0.113.10"},
		{"bad scheme", "ftp://192.168.1.254"},
		{"no scheme", "mafreebox.freebox.fr"},
		{"missing host", "http:///"},
		{"unparsable", "http://%zz"},
		// Every one of these resolves to a real host in production, but none
		// of them is where a Freebox lives — accepting them would keep
		// exactly the internal scan/POST primitive this validator exists to
		// close (loopback, cloud metadata, the unspecified address,
		// link-local).
		{"loopback IPv4", "http://127.0.0.1"},
		{"cloud metadata", "http://169.254.169.254/"},
		{"loopback IPv6", "http://[::1]"},
		{"unspecified IPv4", "http://0.0.0.0"},
		{"link-local IPv6", "http://[fe80::1]"},
	}

	for _, tc := range cases {
		err := freebox.ValidateBaseURL(tc.raw)
		r.Error(err, tc.name)
		r.ErrorIs(err, freebox.ErrBaseURLInvalid, tc.name)
	}
}
