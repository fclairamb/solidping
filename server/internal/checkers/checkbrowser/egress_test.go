package checkbrowser

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkbrowser/config"
	"github.com/fclairamb/solidping/server/internal/egress"
)

func denyWithLookup(t *testing.T, ips ...string) context.Context {
	t.Helper()

	return egress.WithGuard(t.Context(), egress.New(false, egress.WithLookup(
		func(context.Context, string) ([]net.IPAddr, error) {
			if len(ips) == 0 {
				return nil, &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}
			}

			out := make([]net.IPAddr, 0, len(ips))
			for _, ip := range ips {
				out = append(out, net.IPAddr{IP: net.ParseIP(ip)})
			}

			return out, nil
		},
	)))
}

// The direct case: a URL whose host is, or resolves to, a non-public address.
func TestPreflightEgressRefusesNonPublicHosts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	deny := egress.WithGuard(t.Context(), egress.New(false))

	for _, rawURL := range []string{
		"http://127.0.0.1:4000/api/v1/",
		"http://169.254.169.254/latest/meta-data/",
		"https://[::1]/",
		"https://[::ffff:127.0.0.1]/",
	} {
		_, err := preflightEgress(deny, rawURL)
		r.ErrorIs(err, egress.ErrDenied, rawURL)
	}

	_, err := preflightEgress(denyWithLookup(t, "10.0.0.5"), "https://internal.acme.com/")
	r.ErrorIs(err, egress.ErrDenied)
	r.NotContains(err.Error(), "10.0.0.5", "the resolved internal address is never echoed")
}

// 1a: a URL Chrome would read with a host we would not see.
func TestPreflightEgressRefusesHostlessAndNonHTTPURLs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	deny := egress.WithGuard(t.Context(), egress.New(false))

	for _, rawURL := range []string{
		"http:127.0.0.1:4000/",
		`http:\\127.0.0.1\`,
		"http:///127.0.0.1:4000/",
		"about:blank",
		"file:///etc/passwd",
		"ftp://acme.com/",
		"https://%zz/",
	} {
		_, err := preflightEgress(deny, rawURL)
		r.Error(err, rawURL)
	}

	// The same URLs never get past the check's own validation either.
	for _, rawURL := range []string{"http:127.0.0.1:4000/", `http:\\127.0.0.1\`, "http:///127.0.0.1:4000/"} {
		r.Error(checkconfig.ValidateNavigationURL(rawURL), rawURL)
	}

	r.NoError(checkconfig.ValidateNavigationURL("https://acme.com/"))
}

// 1b: the legacy IPv4 spellings a browser reads as 127.0.0.1 are judged as
// 127.0.0.1, not handed to DNS as names.
func TestPreflightEgressReadsLegacyIPv4LikeABrowser(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A resolver that would happily answer PUBLIC for anything: a legacy form
	// must never reach it.
	deny := denyWithLookup(t, "93.184.216.34")

	for _, host := range []string{
		"2130706433", "0x7f000001", "0X7F000001", "0177.0.0.1", "127.1", "127.0.1",
		"0x7f.0.0.1", "0x7f.1", "017700000001", "127.0.0.1.", "0xa9fea9fe", "169.254.43518",
	} {
		_, err := preflightEgress(deny, "http://"+host+":4000/")
		r.ErrorIs(err, egress.ErrDenied, host)
	}

	// Positive control: a legacy spelling of a PUBLIC address passes, unpinned
	// (an IP literal needs no pin).
	pin, err := preflightEgress(deny, "http://134744072/") // 8.8.8.8
	r.NoError(err)
	r.Nil(pin)
}

// Under an enforcing policy a host that does not resolve is refused rather
// than handed to Chrome's own resolver (fail closed).
func TestPreflightEgressFailsClosedOnUnresolvableHosts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := preflightEgress(denyWithLookup(t), "https://unresolvable.invalid/")
	r.ErrorIs(err, errNavigationRefused)

	// Not enforcing: nothing is resolved, nothing is refused.
	allow := egress.WithGuard(t.Context(), egress.New(true))
	pin, err := preflightEgress(allow, "http://127.0.0.1:4000/")
	r.NoError(err)
	r.Nil(pin)

	pin, err = preflightEgress(t.Context(), "http:127.0.0.1/")
	r.NoError(err)
	r.Nil(pin)
}

// 1c: the approved address is pinned for Chrome's resolver.
func TestPreflightEgressPinsTheApprovedAddress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pin, err := preflightEgress(denyWithLookup(t, "10.0.0.1", "93.184.216.34"), "https://www.acme.com/login")
	r.NoError(err)
	r.NotNil(pin)
	r.Equal("MAP www.acme.com 93.184.216.34", pin.resolverRule())

	pin6, err := preflightEgress(denyWithLookup(t, "2606:4700::1"), "https://v6.acme.com/")
	r.NoError(err)
	r.Equal("MAP v6.acme.com [2606:4700::1]", pin6.resolverRule())
}

// 1c backstop: a response Chrome got from a non-public address (a redirect, a
// subresource, a rebound DNS answer) poisons the session — every later action
// returns the refusal and nothing read from the page reaches a result.
func TestSessionIsPoisonedByANonPublicResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ctx, rec := egress.WithRecorder(t.Context())
	guard := egress.New(false)
	session := &Session{}

	session.checkRemote(ctx, guard, "https://www.acme.com/", "93.184.216.34")
	session.checkRemote(ctx, guard, "https://cdn.acme.com/app.js", "")
	r.NoError(session.deniedByEgress(), "public and cached responses are fine")

	session.checkRemote(ctx, guard, "http://rebind.acme.com/secret", "[::1]")
	session.checkRemote(ctx, guard, "http://other.acme.com/", "10.0.0.1")

	err := session.deniedByEgress()
	r.ErrorIs(err, egress.ErrDenied)
	r.Contains(err.Error(), "rebind.acme.com", "the first refusal is the one reported")
	r.NotNil(rec.Denied(), "the execution's recorder sees it too")

	_, navErr := session.Navigate(ctx, "https://www.acme.com/")
	r.ErrorIs(navErr, egress.ErrDenied)

	_, textErr := session.Text(ctx, "body")
	r.ErrorIs(textErr, egress.ErrDenied)
}
