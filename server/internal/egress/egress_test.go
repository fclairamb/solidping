package egress_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

var errLookupBroken = errors.New("resolver broken")

// fixedLookup answers every name with the same addresses and counts calls.
func fixedLookup(calls *atomic.Int32, ips ...string) egress.LookupFunc {
	return func(_ context.Context, _ string) ([]net.IPAddr, error) {
		calls.Add(1)

		out := make([]net.IPAddr, 0, len(ips))
		for _, ip := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(ip)})
		}

		return out, nil
	}
}

func TestIsNonPublic(t *testing.T) {
	t.Parallel()

	nonPublic := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", "169.254.0.1",
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.1.1",
		"100.64.0.1", "100.100.100.200",
		"0.0.0.0", "0.1.2.3", "::",
		"fc00::1", "fd12:3456::1",
		"fe80::1", "febf::1",
		"fec0::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::a9fe:a9fe", // NAT64 of 169.254.169.254
		"64:ff9b::7f00:1",    // NAT64 of 127.0.0.1
		"224.0.0.1", "239.255.255.250", "255.255.255.255", "240.0.0.1",
		"ff02::1",
		"198.18.0.1", "192.0.0.8",
	}
	for _, raw := range nonPublic {
		require.True(t, egress.IsNonPublic(net.ParseIP(raw)), "%s must be non-public", raw)
	}

	public := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34",
		"172.32.0.1", "172.15.255.255", "100.128.0.1", "11.0.0.1",
		"2606:4700:4700::1111", "2001:4860:4860::8888",
		"::ffff:8.8.8.8",
		"64:ff9b::808:808", // NAT64 of 8.8.8.8
	}
	for _, raw := range public {
		require.False(t, egress.IsNonPublic(net.ParseIP(raw)), "%s must be public", raw)
	}

	require.True(t, egress.IsNonPublic(nil), "an unknown address fails closed")
	require.Contains(t, egress.NonPublicRanges(), "169.254.0.0/16")
}

func TestCheckIP(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	deny := egress.New(false)
	allow := egress.New(true)

	var nilGuard *egress.Guard

	for _, raw := range []string{"127.0.0.1", "169.254.169.254", "10.1.2.3", "fd00::1", "fe80::1", "::"} {
		ip := net.ParseIP(raw)

		err := deny.CheckIP(ip)
		r.ErrorIs(err, egress.ErrDenied, raw)

		var denied *egress.DeniedError
		r.ErrorAs(err, &denied)
		r.Equal(raw, denied.Host)

		r.NoError(allow.CheckIP(ip), raw)
		r.NoError(nilGuard.CheckIP(ip), "a nil guard allows everything")
	}

	r.NoError(deny.CheckIP(net.ParseIP("8.8.8.8")))
	r.True(deny.Enforcing())
	r.False(allow.Enforcing())
	r.False(nilGuard.Enforcing())
	r.True(nilGuard.AllowsPrivate())
}

// The message is the user's only clue: it must carry the spec's sentence and
// name BOTH knobs an operator can flip.
func TestDeniedErrorNamesTheOperatorSwitch(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	err := egress.New(false).CheckHostIP("internal.acme.com", net.ParseIP("10.0.0.5"))
	r.Error(err)

	msg := err.Error()
	r.Contains(msg, "target resolves to a non-public address, denied by egress policy")
	r.Contains(msg, "internal.acme.com (10.0.0.5)")
	r.Contains(msg, egress.EnvAllowPrivate+"=true")
	r.Contains(msg, egress.ParamAllowPrivate)
}

// A hostname resolving to a private address is the rebinding/SSRF case a
// URL-string blocklist misses: the guard judges the RESOLVED answer.
func TestResolveRefusesAHostnameResolvingPrivate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var calls atomic.Int32

	deny := egress.New(false, egress.WithLookup(fixedLookup(&calls, "169.254.169.254")))

	_, err := deny.Resolve(t.Context(), "metadata.acme.com")
	r.ErrorIs(err, egress.ErrDenied)
	r.Contains(err.Error(), "metadata.acme.com (169.254.169.254)")

	// Mixed answers: only the public ones survive, in resolver order.
	mixed := egress.New(false, egress.WithLookup(fixedLookup(&calls, "10.0.0.1", "93.184.216.34", "fd00::1")))
	ips, err := mixed.Resolve(t.Context(), "mixed.acme.com")
	r.NoError(err)
	r.Len(ips, 1)
	r.Equal("93.184.216.34", ips[0].String())

	// Permissive: every answer is returned.
	allow := egress.New(true, egress.WithLookup(fixedLookup(&calls, "10.0.0.1", "93.184.216.34")))
	ips, err = allow.Resolve(t.Context(), "mixed.acme.com")
	r.NoError(err)
	r.Len(ips, 2)

	// IP literals, bracketed or not, never hit the resolver.
	before := calls.Load()
	_, err = deny.Resolve(t.Context(), "[::1]")
	r.ErrorIs(err, egress.ErrDenied)
	_, err = deny.Resolve(t.Context(), "127.0.0.1")
	r.ErrorIs(err, egress.ErrDenied)
	r.Equal(before, calls.Load())

	// A resolver failure is passed through, not turned into a refusal.
	broken := egress.New(false, egress.WithLookup(func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errLookupBroken
	}))
	_, err = broken.Resolve(t.Context(), "acme.com")
	r.ErrorIs(err, errLookupBroken)
	r.NotErrorIs(err, egress.ErrDenied)
}

func listenLoopback(t *testing.T) (net.Listener, *atomic.Int32) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var accepted atomic.Int32

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)

			_ = conn.Close()
		}
	}()

	return ln, &accepted
}

// The guard resolves ONCE through its own resolver ("pinned.invalid" exists in
// no DNS) and refuses the loopback answer without connecting.
func TestDialContextResolvesOnceAndRefusesBeforeConnect(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ln, accepted := listenLoopback(t)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	r.NoError(err)

	var calls atomic.Int32

	// A permissive guard is byte-for-byte the plain dialer: it connects to the
	// loopback listener and never consults its resolver.
	allow := egress.New(true, egress.WithLookup(fixedLookup(&calls, "127.0.0.1")))

	conn, err := allow.DialContext(t.Context(), "tcp", ln.Addr().String())
	r.NoError(err)
	r.NoError(conn.Close())
	r.Equal(int32(0), calls.Load())

	// Enforcing: the name that resolves to loopback is refused before any
	// connect, after exactly one resolution.
	deny := egress.New(false, egress.WithLookup(fixedLookup(&calls, "127.0.0.1")))

	conn, err = deny.DialContext(t.Context(), "tcp", net.JoinHostPort("pinned.invalid", port))
	r.ErrorIs(err, egress.ErrDenied)
	r.Nil(conn)
	r.Equal(int32(1), calls.Load(), "resolved exactly once")
	require.Eventually(t, func() bool { return accepted.Load() == 1 }, time.Second, 10*time.Millisecond,
		"only the permissive dial reached the listener")
}

// The positive control of the test above: an enforcing guard whose resolver
// answers with a public address dials THAT address. A public address cannot
// be reached from a test, so the dial is observed through the base dialer's
// Control hook, which sees the literal connect target.
func TestEnforcingDialUsesThePinnedPublicAddress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var calls atomic.Int32

	deny := egress.New(false, egress.WithLookup(fixedLookup(&calls, "10.0.0.1", "93.184.216.34")))

	var connectedTo atomic.Value

	errStop := errors.New("stop before connect") //nolint:err113 // test sentinel

	base := &net.Dialer{
		Timeout: time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			connectedTo.Store(address)

			return errStop
		},
	}

	_, err := deny.DialContextWith(t.Context(), base, "tcp", "rebind.acme.com:443")
	r.ErrorIs(err, errStop)
	r.Equal("93.184.216.34:443", connectedTo.Load(), "the private answer is dropped, the public one pinned")
	r.Equal(int32(1), calls.Load())
}

func TestControlRefusesNonPublicConnects(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	deny := egress.New(false)
	r.ErrorIs(deny.Control("tcp4", "127.0.0.1:80", nil), egress.ErrDenied)
	r.ErrorIs(deny.Control("tcp6", "[fe80::1]:80", nil), egress.ErrDenied)
	r.NoError(deny.Control("tcp4", "8.8.8.8:53", nil))
	r.NoError(egress.New(true).Control("tcp4", "127.0.0.1:80", nil))

	// Through a real dialer: the literal loopback connect never happens.
	ln, accepted := listenLoopback(t)
	dialer := &net.Dialer{Control: deny.ControlContext(t.Context())}

	_, err := dialer.DialContext(t.Context(), "tcp", ln.Addr().String())
	r.ErrorIs(err, egress.ErrDenied)
	r.Equal(int32(0), accepted.Load())
}

func TestDialContextFamilyFilter(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	deny := egress.New(false, egress.WithLookup(fixedLookup(&calls, "93.184.216.34")))

	_, err := deny.DialContext(t.Context(), "tcp6", "v4only.acme.com:80")

	var addrErr *net.AddrError
	require.ErrorAs(t, err, &addrErr)
}

func TestRecorderKeepsTheFirstRefusal(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ctx, rec := egress.WithRecorder(t.Context())
	r.Nil(rec.Denied())

	deny := egress.New(false)
	r.Error(deny.CheckAddr(ctx, "first.acme.com", net.ParseIP("10.0.0.1")))
	r.Error(deny.CheckAddr(ctx, "second.acme.com", net.ParseIP("10.0.0.2")))

	r.NotNil(rec.Denied())
	r.Equal("first.acme.com", rec.Denied().Host)

	// A refusal reported by a library on its own context is carried over.
	ctx2, rec2 := egress.WithRecorder(t.Context())
	egress.RecordDenial(ctx2, &net.OpError{Op: "dial", Err: &egress.DeniedError{Host: "x", IP: net.ParseIP("::1")}})
	r.NotNil(rec2.Denied())

	egress.RecordDenial(ctx2, errLookupBroken)
	r.Equal("x", rec2.Denied().Host)

	var nilRec *egress.Recorder
	r.Nil(nilRec.Denied())
}

func TestContextCarriesTheGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(egress.FromContext(t.Context()))

	g := egress.New(false)
	r.Same(g, egress.FromContext(egress.WithGuard(t.Context(), g)))
}

// The shared HTTP transport: every request dials through the guard, and the
// same transport is handed out every time (connection pooling survives).
func TestHTTPTransport(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret-internal-body"))
	}))
	t.Cleanup(server.Close)

	deny := egress.New(false)
	r.Same(deny.HTTPTransport(), deny.HTTPTransport())
	r.Nil(deny.HTTPTransport().Proxy, "a proxy would connect on our behalf, out of the guard's sight")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	r.NoError(err)

	resp, err := (&http.Client{Transport: deny.HTTPTransport()}).Do(req) //nolint:bodyclose // errors, no body
	r.ErrorIs(err, egress.ErrDenied)
	r.Nil(resp)
	r.False(strings.Contains(err.Error(), "secret-internal-body"))

	allowResp, err := (&http.Client{Transport: egress.New(true).HTTPTransport()}).Do(req)
	r.NoError(err)

	defer func() { _ = allowResp.Body.Close() }()

	r.Equal(http.StatusOK, allowResp.StatusCode)
}
