package tools

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

func TestHTTPDetailsCarryTheCertificateExpiryOverHTTPS(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{NotAfter: now.Add(41*24*time.Hour + time.Hour)}}}

	res := &checkerdef.Result{Duration: 182 * time.Millisecond, Output: map[string]any{
		checkerdef.OutputKeyStatusCode: 200, "redirect_chain": []string{"https://www.acme.com/"},
	}}

	details := httpDetails(res, "http://acme.com/", state, now)
	r.Equal(200, details["statusCode"])
	r.Equal("https://www.acme.com/", details["finalUrl"])
	r.Equal(41, details["certDaysLeft"])
	r.Equal("2026-11-24T11:00:00Z", details["certNotAfter"])
	r.Equal(int64(182), details["responseTimeMs"])

	plain := httpDetails(res, "http://acme.com/", nil, now)
	r.NotContains(plain, "certDaysLeft")
}

// publicIP is the IPv6 loopback, which these tests have the guard treat as a
// public address (egress.WithPublicOverride): a test cannot reach a real
// public host, and every other address (127.0.0.1 included) is judged exactly
// as in production.
var publicIP = net.IPv6loopback //nolint:gochecknoglobals // test fixture

// fakeDNS answers from a fixed table; an unknown name is NXDOMAIN.
func fakeDNS(table map[string][]string) egress.LookupFunc {
	return func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}

		out := make([]net.IPAddr, 0, len(ips))
		for _, ip := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(ip)})
		}

		return out, nil
	}
}

// rebindingDNS answers a public address on the first lookup of a name and a
// private one on every later lookup.
func rebindingDNS(calls *atomic.Int32) egress.LookupFunc {
	return func(_ context.Context, _ string) ([]net.IPAddr, error) {
		if calls.Add(1) == 1 {
			return []net.IPAddr{{IP: publicIP}}, nil
		}

		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
}

// fakeDNSTool answers the DNS tool from the same table as lookup, without
// network: a name lookup does not know is a DOWN result (NXDOMAIN).
func fakeDNSTool(lookup egress.LookupFunc) DNSResolveFunc {
	return func(ctx context.Context, _, host, record string) (*checkerdef.Result, error) {
		addrs, err := lookup(ctx, host)
		if err != nil {
			//nolint:nilerr // NXDOMAIN is a DOWN result, as checkdns reports it
			return &checkerdef.Result{Status: checkerdef.StatusDown, Output: map[string]any{
				checkerdef.OutputKeyError: err.Error(),
			}}, nil
		}

		ips := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			ips = append(ips, addr.IP.String())
		}

		key := "resolved_ips"
		if record != "A" && record != "AAAA" {
			key = "resolved_values"
		}

		return &checkerdef.Result{Status: checkerdef.StatusUp, Output: map[string]any{key: ips}}, nil
	}
}

func newTestService(lookup egress.LookupFunc, opts ...Option) *Service {
	opts = append([]Option{
		WithLookup(lookup),
		WithDNSResolver(fakeDNSTool(lookup)),
		WithGuardOptions(egress.WithPublicOverride(publicIP)),
		WithTimeout(3 * time.Second),
	}, opts...)

	return NewService(Params{DeniedHosts: []string{"solidping.io"}, Region: "eu-test"}, opts...)
}

// ipv6Server serves handler on [::1] (the "public" address of these tests).
func ipv6Server(t *testing.T, handler http.Handler) int {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	return tcpPort(t, listener.Addr())
}

func tcpPort(t *testing.T, addr net.Addr) int {
	t.Helper()

	tcpAddr, ok := addr.(*net.TCPAddr)
	require.True(t, ok)

	return tcpAddr.Port
}

func targetFor(typ, host string) string {
	switch typ {
	case TypeHTTP:
		return "http://" + hostPort(host, "") + "/"
	case TypeSSL:
		return hostPort(host, "443")
	default:
		return hostPort(host, "80")
	}
}

func TestEveryTypeRefusesNonPublicTargets(t *testing.T) {
	t.Parallel()

	dns := fakeDNS(map[string][]string{
		"loopback.test": {"127.0.0.1"},
		"metadata.test": {"169.254.169.254"},
		"mixed.test":    {publicIP.String(), "10.0.0.1"},
		"lan.test":      {"192.168.1.10"},
	})

	hosts := []string{
		"loopback.test", "metadata.test", "mixed.test", "lan.test",
		"127.0.0.1", "169.254.169.254", "2130706433", "0x7f000001", "127.1", "::ffff:127.0.0.1", "::1",
	}

	for _, typ := range []string{TypeHTTP, TypeSSL, TypeTCP} {
		for _, host := range hosts {
			t.Run(typ+"/"+host, func(t *testing.T) {
				t.Parallel()

				svc := newTestService(dns)
				// ::1 is "public" for the other tests; here it must be refused,
				// so this service judges it as production does.
				if host == "::1" {
					svc = NewService(Params{}, WithLookup(dns))
				}

				result, _, err := svc.Run(t.Context(), &Request{Type: typ, Target: targetFor(typ, host)})
				require.ErrorIs(t, err, ErrTargetNotAllowed, "result: %+v", result)
			})
		}
	}
}

// Positive control for the test above: the same service reaches a target the
// guard judges public.
func TestPublicTargetIsReached(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	port := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))

	result, cached, err := svc.Run(t.Context(),
		&Request{Type: "HTTP", Target: fmt.Sprintf("http://public.test:%d/x", port)})
	r.NoError(err)
	r.False(cached)
	r.Equal("up", result.Status)
	r.Equal("eu-test", result.Region)
	r.Equal(http.StatusOK, result.Details["statusCode"])
	r.Equal(fmt.Sprintf("http://public.test:%d/x", port), result.Details["finalUrl"])
	r.Equal(TypeHTTP, result.Monitor.CheckType)
	r.Contains(result.Monitor.Query, "checkType=http")
	r.Contains(result.Monitor.Query, "httpUrl=http%3A%2F%2Fpublic.test")
}

func TestRebindingCannotReachAPrivateAddress(t *testing.T) {
	t.Parallel()

	t.Run("http re-resolves at dial and is refused there", func(t *testing.T) {
		t.Parallel()

		var calls atomic.Int32

		svc := newTestService(rebindingDNS(&calls))

		_, _, err := svc.Run(t.Context(), &Request{Type: TypeHTTP, Target: "http://rebind.test/"})
		require.ErrorIs(t, err, ErrTargetNotAllowed)
		require.GreaterOrEqual(t, calls.Load(), int32(2), "the dial resolved again")
	})

	// ssl and tcp dial the address the guard vetted, so there is no second
	// answer for a rebinding resolver to change: the private listener is
	// never reached.
	for _, typ := range []string{TypeSSL, TypeTCP} {
		t.Run(typ+" pins the vetted address", func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			private, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			r.NoError(err)
			t.Cleanup(func() { _ = private.Close() })

			var accepted atomic.Int32

			go func() {
				for {
					conn, acceptErr := private.Accept()
					if acceptErr != nil {
						return
					}

					accepted.Add(1)
					_ = conn.Close()
				}
			}()

			var calls atomic.Int32

			svc := newTestService(rebindingDNS(&calls))
			port := tcpPort(t, private.Addr())

			result, _, err := svc.Run(t.Context(), &Request{Type: typ, Target: "rebind.test:" + strconv.Itoa(port)})
			if err == nil {
				r.NotEqual("up", result.Status, "nothing listens on the vetted address")
			}

			r.Equal(int32(1), calls.Load(), "the name was resolved once")
			r.Equal(int32(0), accepted.Load(), "the private address was never dialed")
		})
	}
}

func TestRedirectToMetadataIsRefusedAtTheHop(t *testing.T) {
	t.Parallel()

	port := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))

	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))

	_, _, err := svc.Run(t.Context(), &Request{Type: TypeHTTP, Target: fmt.Sprintf("http://public.test:%d/", port)})
	require.ErrorIs(t, err, ErrTargetNotAllowed)
}

func TestSolidPingOwnHostsAreRefused(t *testing.T) {
	t.Parallel()

	svc := newTestService(fakeDNS(map[string][]string{
		"solidping.io": {publicIP.String()}, "api.solidping.io": {publicIP.String()},
	}))

	for _, req := range []Request{
		{Type: TypeHTTP, Target: "https://solidping.io/api/v1/orgs"},
		{Type: TypeHTTP, Target: "api.SolidPing.io"},
		{Type: TypeSSL, Target: "api.solidping.io"},
		{Type: TypeTCP, Target: "solidping.io:5432"},
	} {
		_, _, err := svc.Run(t.Context(), &req)
		require.ErrorIs(t, err, ErrTargetNotAllowed, req.Target)
	}

	t.Run("through a redirect", func(t *testing.T) {
		t.Parallel()

		port := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "https://api.solidping.io/", http.StatusFound)
		}))

		redirecting := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))

		_, _, err := redirecting.Run(t.Context(), &Request{
			Type: TypeHTTP, Target: fmt.Sprintf("http://public.test:%d/", port),
		})
		require.ErrorIs(t, err, ErrTargetNotAllowed)
	})
}

func TestHTTPFollowsAtMostFiveRedirects(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hits atomic.Int32

	port := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		http.Redirect(w, req, req.URL.Path+"x", http.StatusFound)
	}))

	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))

	result, _, err := svc.Run(t.Context(), &Request{Type: TypeHTTP, Target: fmt.Sprintf("http://public.test:%d/", port)})
	r.NoError(err)
	r.Equal("down", result.Status)
	r.Len(result.Details["redirects"], MaxRedirects)
	r.Equal(int32(MaxRedirects+1), hits.Load())
}

func TestIdenticalRunsWithinTheTTLMakeOneRequest(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hits atomic.Int32

	port := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	now := time.Now()
	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))
	svc.now = func() time.Time { return now }

	req := &Request{Type: TypeHTTP, Target: fmt.Sprintf("http://public.test:%d/", port)}

	_, cached, err := svc.Run(t.Context(), req)
	r.NoError(err)
	r.False(cached)

	_, cached, err = svc.Run(t.Context(), req)
	r.NoError(err)
	r.True(cached)
	r.Equal(int32(1), hits.Load(), "the second run inside 60s is served from the cache")

	now = now.Add(CacheTTL + time.Second)

	_, cached, err = svc.Run(t.Context(), req)
	r.NoError(err)
	r.False(cached)
	r.Equal(int32(2), hits.Load(), "an expired entry runs again")
}

func TestTCPRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	r.NoError(err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))
	port := strconv.Itoa(tcpPort(t, listener.Addr()))

	result, _, err := svc.Run(t.Context(), &Request{Type: TypeTCP, Target: "public.test:" + port})
	r.NoError(err)
	r.Equal("up", result.Status)
	r.Equal("public.test:"+port, result.Target)
	r.Equal(true, result.Details["connected"])
	r.Contains(result.Details, "connectTimeMs")
	r.Contains(result.Monitor.Query, "port="+port)
}

func TestSSLRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	r.NoError(err)

	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	_ = server.Listener.Close()
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)

	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))
	port := strconv.Itoa(tcpPort(t, listener.Addr()))

	// httptest's certificate is self-signed: the handshake reached the target
	// (the guard let it through) and the verdict says why it is not trusted.
	result, _, err := svc.Run(t.Context(), &Request{Type: TypeSSL, Target: "https://public.test:" + port + "/path"})
	r.NoError(err)
	r.Equal("public.test:"+port, result.Target)
	r.Equal("down", result.Status)
	r.Contains(result.Details["error"], "certificate")
	r.Contains(result.Monitor.Query, "checkType=ssl")
	r.Contains(result.Monitor.Query, "port="+port)
}

func TestSSLDetailsMapping(t *testing.T) {
	t.Parallel()

	details := sslDetails(&checkerdef.Result{Output: map[string]any{
		"issuer": "R3", "subject": "acme.com", "not_before": "2026-01-01T00:00:00Z",
		"not_after": "2026-12-01T00:00:00Z", "days_remaining": 41, "dns_names": []string{"acme.com"},
		"chainVerified": true, "tls_version": "TLS 1.3", "serial_number": "1",
	}})

	require.Equal(t, map[string]any{
		"issuer": "R3", "subject": "acme.com", "notBefore": "2026-01-01T00:00:00Z",
		"notAfter": "2026-12-01T00:00:00Z", "daysLeft": 41, "sans": []string{"acme.com"},
		"chainValid": true, "tlsVersion": "TLS 1.3",
	}, details)
}

func TestDNSRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var nameservers []string

	lookup := fakeDNS(map[string][]string{"acme.com": {"8.8.4.4"}})
	resolve := fakeDNSTool(lookup)
	svc := newTestService(lookup, WithDNSResolver(
		func(ctx context.Context, nameserver, host, record string) (*checkerdef.Result, error) {
			nameservers = append(nameservers, nameserver)
			if nameserver == "1.1.1.1:53" {
				return nil, errFakeTimeout
			}

			return resolve(ctx, nameserver, host, record)
		}))

	result, _, err := svc.Run(t.Context(), &Request{Type: TypeDNS, Target: "acme.com"})
	r.NoError(err)
	r.Equal("up", result.Status)
	r.Equal("A", result.Details["record"])
	r.Equal([]string{"8.8.4.4"}, result.Details["records"])
	r.Equal("checkName=acme.com&checkType=dns&host=acme.com", result.Monitor.Query)
	r.Equal([]string{"1.1.1.1:53", "8.8.8.8:53"}, nameservers, "public resolvers, never the system one")
}

var errFakeTimeout = errors.New("i/o timeout")

// The DNS tool must not reveal what an internal resolver knows: internal
// names are refused before any query, private answers after it.
func TestDNSRefusesInternalNamesAndPrivateAnswers(t *testing.T) {
	t.Parallel()

	lookup := fakeDNS(map[string][]string{
		"private.acme.com":   {"10.1.2.3"},
		"loopback.acme.com":  {"::1"},
		"linklocal.acme.com": {"169.254.169.254"},
		"mixed.acme.com":     {"8.8.4.4", "192.168.1.10"},
	})
	resolve := fakeDNSTool(lookup)
	svc := newTestService(lookup, WithDNSResolver(
		func(ctx context.Context, nameserver, host, record string) (*checkerdef.Result, error) {
			if host == "alias.acme.com" {
				return &checkerdef.Result{Status: checkerdef.StatusUp, Output: map[string]any{
					"resolved_values": []string{"db.svc.cluster.local"},
				}}, nil
			}

			return resolve(ctx, nameserver, host, record)
		}))

	for _, req := range []Request{
		{Type: TypeDNS, Target: "localhost"},
		{Type: TypeDNS, Target: "intranet"},
		{Type: TypeDNS, Target: "printer.local"},
		{Type: TypeDNS, Target: "vault.internal"},
		{Type: TypeDNS, Target: "db.default.svc.cluster.local"},
		{Type: TypeDNS, Target: "app.localhost"},
		{Type: TypeDNS, Target: "private.acme.com"},
		{Type: TypeDNS, Target: "loopback.acme.com", Options: Options{Record: "AAAA"}},
		{Type: TypeDNS, Target: "linklocal.acme.com"},
		{Type: TypeDNS, Target: "mixed.acme.com"},
		{Type: TypeDNS, Target: "alias.acme.com", Options: Options{Record: "CNAME"}},
	} {
		_, _, err := svc.Run(t.Context(), &req)
		require.ErrorIs(t, err, ErrTargetNotAllowed, "%+v", req)
	}

	// Positive control: a public answer goes through.
	public := newTestService(fakeDNS(map[string][]string{"public.acme.com": {"8.8.4.4"}}))
	_, _, err := public.Run(t.Context(), &Request{Type: TypeDNS, Target: "public.acme.com"})
	require.NoError(t, err)
}

func TestInvalidTargets(t *testing.T) {
	t.Parallel()

	svc := newTestService(fakeDNS(nil))

	for _, req := range []Request{
		{Type: "browser", Target: "https://example.com"},
		{Type: TypeHTTP, Target: ""},
		{Type: TypeHTTP, Target: "ftp://example.com"},
		{Type: TypeHTTP, Target: "https://user:pass@example.com"},
		{Type: TypeHTTP, Target: "https://exa mple.com"},
		{Type: TypeHTTP, Target: "https://" + strings.Repeat("a", maxTargetLength)},
		{Type: TypeTCP, Target: "example.com"},
		{Type: TypeTCP, Target: "example.com:0"},
		{Type: TypeTCP, Target: "example.com:65536"},
		{Type: TypeTCP, Target: "example.com:http"},
		{Type: TypeSSL, Target: "example.com:99999"},
		{Type: TypeDNS, Target: "127.0.0.1"},
		{Type: TypeDNS, Target: "example.com", Options: Options{Record: "SOA"}},
		{Type: TypeDNS, Target: "example.com", Options: Options{Record: "ANY"}},
	} {
		_, _, err := svc.Run(t.Context(), &req)
		require.ErrorIs(t, err, ErrInvalidTarget, "%+v", req)
	}
}

func TestUnresolvableTargetIsResolveFailed(t *testing.T) {
	t.Parallel()

	svc := newTestService(fakeDNS(nil))

	_, _, err := svc.Run(t.Context(), &Request{Type: TypeTCP, Target: "nowhere.test:443"})
	require.ErrorIs(t, err, ErrResolveFailed)
}

func TestSlowTargetTimesOut(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	port := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { close(release) })

	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}),
		WithTimeout(200*time.Millisecond))

	_, _, err := svc.Run(t.Context(), &Request{Type: TypeHTTP, Target: fmt.Sprintf("http://public.test:%d/", port)})
	require.ErrorIs(t, err, ErrTimeout)
}

func TestRunsBeyondTheConcurrencyCapAreRefused(t *testing.T) {
	t.Parallel()

	svc := newTestService(fakeDNS(map[string][]string{"public.test": {publicIP.String()}}))
	for range MaxConcurrentRuns {
		svc.slots <- struct{}{}
	}

	_, _, err := svc.Run(t.Context(), &Request{Type: TypeTCP, Target: "public.test:80"})
	require.ErrorIs(t, err, ErrBusy)
}
