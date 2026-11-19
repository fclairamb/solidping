package checkbrowser

import (
	"context"
	"net"
	"testing"

	"github.com/chromedp/cdproto/network"
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

	session.checkRemote(ctx, guard, "https://www.acme.com/", "93.184.216.34", 443)
	session.checkRemote(ctx, guard, "https://cdn.acme.com/app.js", "", 443)
	r.NoError(session.deniedByEgress(), "public and cached responses are fine")

	session.checkRemote(ctx, guard, "http://rebind.acme.com/secret", "[::1]", 443)
	session.checkRemote(ctx, guard, "http://other.acme.com/", "10.0.0.1", 443)

	err := session.deniedByEgress()
	r.ErrorIs(err, egress.ErrDenied)
	r.Contains(err.Error(), "rebind.acme.com", "the first refusal is the one reported")
	r.NotNil(rec.Denied(), "the execution's recorder sees it too")

	_, navErr := session.Navigate(ctx, "https://www.acme.com/")
	r.ErrorIs(navErr, egress.ErrDenied)

	_, textErr := session.Text(ctx, "body")
	r.ErrorIs(textErr, egress.ErrDenied)
}

// A WebSocket the page opens is judged on its host (spec 2026-11-19-04):
// Chrome reports no remote address for one, and the response listener never
// sees it.
func TestSessionIsPoisonedByAWebSocketToAPrivateHost(t *testing.T) {
	t.Parallel()

	publicOnly := func(host string) ([]net.IPAddr, error) {
		if host == "internal.acme.com" {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.1.2.3")}}, nil
		}

		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	guard := egress.New(false, egress.WithLookup(func(_ context.Context, host string) ([]net.IPAddr, error) {
		return publicOnly(host)
	}))

	t.Run("public host", func(t *testing.T) {
		t.Parallel()

		session := &Session{}
		session.onEgressEvent(t.Context(), guard, &network.EventWebSocketCreated{URL: "wss://ws.acme.com/feed"})
		require.NoError(t, session.deniedByEgress())
	})

	for name, wsURL := range map[string]string{
		"IP literal":                   "ws://10.0.0.5:8080/",
		"legacy IPv4 literal":          "ws://2130706433/",
		"IPv6 loopback":                "ws://[::1]:9222/devtools/browser/x",
		"name with one private answer": "wss://internal.acme.com/socket",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, rec := egress.WithRecorder(t.Context())
			session := &Session{}

			session.onEgressEvent(ctx, guard, &network.EventWebSocketCreated{URL: wsURL})

			err := session.deniedByEgress()
			require.ErrorIs(t, err, egress.ErrDenied)
			require.NotNil(t, rec.Denied(), "the execution's recorder sees it too")

			_, evalErr := session.Evaluate(ctx, "document.title")
			require.ErrorIs(t, evalErr, egress.ErrDenied, "nothing read from the page reaches a result")
		})
	}
}

// Behind the egress proxy, the proxy is the judge: responses Chrome reports
// from the proxy's own loopback address are not refused, and the WebSocket
// host lookup is skipped.
func TestSessionTrustsItsEgressProxy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	guard := egress.New(false)
	session := &Session{}

	proxy, err := startEgressProxy(t.Context(), guard, nil)
	r.NoError(err)

	defer proxy.Close()

	session.proxy = proxy

	addr, ok := proxy.listener.Addr().(*net.TCPAddr)
	r.True(ok)

	session.onEgressEvent(t.Context(), guard, &network.EventResponseReceived{Response: &network.Response{
		URL: "https://www.acme.com/", RemoteIPAddress: addr.IP.String(), RemotePort: int64(addr.Port),
	}})
	session.onEgressEvent(t.Context(), guard, &network.EventWebSocketCreated{URL: "ws://10.0.0.5/"})
	r.NoError(session.deniedByEgress())

	// Another loopback port is not the proxy: still refused.
	session.onEgressEvent(t.Context(), guard, &network.EventResponseReceived{Response: &network.Response{
		URL: "http://127.0.0.1:6379/", RemoteIPAddress: addr.IP.String(), RemotePort: int64(addr.Port) + 1,
	}})
	r.ErrorIs(session.deniedByEgress(), egress.ErrDenied)
}

// The resolver rules of an enforcing local Chrome deny every name by default:
// only the pinned navigation host maps somewhere, and the proxy's address is
// left alone so Chrome can reach it.
func TestEgressResolverRulesDenyEveryOtherHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pin := &hostPin{host: "www.acme.com", ip: net.ParseIP("93.184.216.34")}

	r.Equal("MAP www.acme.com 93.184.216.34, MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
		egressResolverRules(pin, "127.0.0.1"))
	r.Equal("MAP * ~NOTFOUND, EXCLUDE 127.0.0.1", egressResolverRules(nil, "127.0.0.1"),
		"a JS check has no pinned host: every name goes through the proxy")
}

func TestProxyReachableFrom(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(proxyReachableFrom(Settings{}), "a Chrome started here")
	r.True(proxyReachableFrom(Settings{CDPURL: "ws://127.0.0.1:9222"}), "the Kubernetes sidecar")
	r.True(proxyReachableFrom(Settings{CDPURL: "ws://localhost:9222"}))
	r.True(proxyReachableFrom(Settings{CDPURL: "ws://[::1]:9222"}))
	r.False(proxyReachableFrom(Settings{CDPURL: "ws://browser:9222"}), "a compose service elsewhere")
	r.False(proxyReachableFrom(Settings{CDPURL: "ws://10.0.0.4:9222"}))
}
