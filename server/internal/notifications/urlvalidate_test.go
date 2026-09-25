package notifications

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// privateHostLookup answers a fixed set of hostnames with private/loopback
// addresses and everything else with a public one, so the "RFC1918/localhost
// hostname" cases don't depend on the test machine's real resolver or
// /etc/hosts.
func privateHostLookup(t *testing.T) egress.LookupFunc {
	t.Helper()

	answers := map[string]string{
		"localhost":                "127.0.0.1",
		"internal.corp.test":       "10.0.0.5",
		"metadata.google.internal": "169.254.169.254",
	}

	return func(_ context.Context, host string) ([]net.IPAddr, error) {
		ip, ok := answers[host]
		if !ok {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}

		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
}

// TestValidateSenderURL is the table-driven validator coverage the spec asks
// for: syntax rejections (scheme, userinfo, missing host), IP-literal and
// resolved-hostname policy rejections under an enforcing guard, and
// acceptance once allow_private_targets flips the guard off.
func TestValidateSenderURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	enforcing := egress.New(false, egress.WithLookup(privateHostLookup(t)))
	permissive := egress.New(true, egress.WithLookup(privateHostLookup(t)))

	cases := []struct {
		name    string
		url     string
		guard   *egress.Guard
		wantErr bool
	}{
		{"http accepted", "http://example.com/hook", enforcing, false},
		{"https accepted", "https://example.com/hook", enforcing, false},
		{"file scheme rejected", "file:///etc/passwd", enforcing, true},
		{"gopher scheme rejected", "gopher://example.com/", enforcing, true},
		{"no scheme rejected", "example.com/hook", enforcing, true},
		{"userinfo rejected", "http://user:pass@example.com/hook", enforcing, true},
		{"no host rejected", "http:///hook", enforcing, true},
		{"malformed url rejected", "http://[::1", enforcing, true},
		{"metadata ip literal rejected", "http://169.254.169.254/latest/meta-data", enforcing, true},
		{"loopback ip literal rejected", "http://127.0.0.1:8080/hook", enforcing, true},
		{"rfc1918 ip literal rejected", "http://10.1.2.3/hook", enforcing, true},
		{"numeric decimal loopback rejected", "http://2130706433/hook", enforcing, true},
		{"numeric hex loopback rejected", "http://0x7f000001/hook", enforcing, true},
		{"short-form loopback rejected", "http://127.1/hook", enforcing, true},
		{"localhost hostname rejected", "http://localhost/hook", enforcing, true},
		{"rfc1918 hostname rejected", "http://internal.corp.test/hook", enforcing, true},
		{"public hostname accepted", "http://example.com/hook", enforcing, false},
		{"public ip literal accepted", "http://93.184.216.34/hook", enforcing, false},
		{"loopback accepted when allow_private=true", "http://127.0.0.1:8080/hook", permissive, false},
		{"rfc1918 hostname accepted when allow_private=true", "http://internal.corp.test/hook", permissive, false},
		{"nil guard accepts everything syntactically valid", "http://127.0.0.1/hook", nil, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateSenderURL(context.Background(), tc.guard, tc.url)
			if tc.wantErr {
				r.Error(err, tc.url)
			} else {
				r.NoError(err, tc.url)
			}
		})
	}
}

// TestValidateSenderURL_WrapsErrSenderURLInvalid checks every rejection —
// whatever its underlying cause — is matched by errors.Is(err,
// ErrSenderURLInvalid), which is what lets integration CRUD map any of them
// to one VALIDATION_ERROR.
func TestValidateSenderURL_WrapsErrSenderURLInvalid(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	guard := egress.New(false, egress.WithLookup(privateHostLookup(t)))

	err := ValidateSenderURL(context.Background(), guard, "http://169.254.169.254/")
	r.ErrorIs(err, ErrSenderURLInvalid)

	var denied *egress.DeniedError
	r.ErrorAs(err, &denied)
	r.Equal("169.254.169.254", denied.Host)

	r.ErrorIs(ValidateSenderURL(context.Background(), guard, "gopher://x/"), ErrSenderURLInvalid)
	r.ErrorIs(ValidateSenderURL(context.Background(), guard, "http://user:pw@x/"), ErrSenderURLInvalid)
}

// TestValidateSenderURL_UnresolvableHostnameDoesNotFailClosed checks that a
// resolver failure (as opposed to a policy denial) does not itself reject the
// URL: the validator is a friendly pre-check, not the security boundary, and
// a transient DNS hiccup at save time must not block saving a legitimate
// integration.
func TestValidateSenderURL_UnresolvableHostnameDoesNotFailClosed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	errNoSuchHost := &net.DNSError{Err: "no such host", Name: "does-not-exist.invalid", IsNotFound: true}
	lookup := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return nil, errNoSuchHost
	}

	guard := egress.New(false, egress.WithLookup(lookup))

	err := ValidateSenderURL(context.Background(), guard, "http://does-not-exist.invalid/hook")
	r.NoError(err)
	r.False(errors.Is(err, ErrSenderURLInvalid))
}
