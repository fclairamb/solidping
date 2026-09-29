package checkhttp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestBuildTransport_AutoSharesTheCheckTransport is the single most important
// backward-compatibility assertion for HTTP: with nothing configured the check
// must run on ONE shared transport, so its connection pool survives across
// executions, and must dial by name so net.Dialer keeps Happy Eyeballs.
//
// What changed on 2026-09-28 (spec 2026-09-28-04) is WHICH shared transport: it
// used to be nil, which makes net/http fall back to http.DefaultTransport — and
// DefaultTransport negotiates HTTP/2, whose canceled-request handling strands
// the connection in the pool ("consuming a concurrency slot until we can confirm
// the server is still responding"). Every later probe then rode the stranded
// connection, turning a ~1-minute network blip on a production worker into 16
// consecutive failing probes. HTTP/1.1 drops the connection of any request that
// did not complete, so a strand cannot outlive the probe that hit it.
func TestBuildTransport_AutoSharesTheCheckTransport(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	transport, ok := buildTransport(nil, false, checkerdef.IPVersionAuto).(*http.Transport)
	r.True(ok, "auto must produce the shared *http.Transport, not a nil sentinel")

	// The zero value of IPVersion is the "never configured" case and must behave
	// identically — identically meaning the SAME object, i.e. pooled.
	zero, ok := buildTransport(nil, false, "").(*http.Transport)
	r.True(ok, "the zero value of IPVersion must produce a transport too")
	r.Same(transport, zero, "auto must share one transport across executions")

	r.False(transport.ForceAttemptHTTP2, "checks must not negotiate HTTP/2 (spec 2026-09-28-04)")
	r.NotNil(transport.DialContext,
		"auto dials by name: Happy Eyeballs comes from net.Dialer, not from HTTP/2")
	r.NotSame(http.DefaultTransport, transport,
		"http.DefaultTransport speaks HTTP/2 — exactly what a probe must not use")

	// Positive controls: each of the three reasons for a PRIVATE transport does
	// produce one, so the shared transport above is about auto and not about a
	// function that always returns the same thing.
	r.NotNil(buildTransport(nil, true, checkerdef.IPVersionAuto))
	r.NotNil(buildTransport(nil, false, checkerdef.IPVersionIPv6))
	r.NotNil(buildTransport(checkerdef.DialerFunc(nil), false, checkerdef.IPVersionAuto))
}

// TestBuildTransport_TunnelWinsOverIPVersion pins the settled tunnel
// interaction. The pair is rejected at write time; if a hand-edited row carries
// both anyway, the tunnel dialer must stay in place rather than being replaced
// by a family-pinned dialer that would bypass the bastion entirely.
func TestBuildTransport_TunnelWinsOverIPVersion(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tunnelUsed := false
	dialer := checkerdef.DialerFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			tunnelUsed = true

			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	)

	transport, ok := buildTransport(dialer, false, checkerdef.IPVersionIPv6).(*http.Transport)
	r.True(ok)
	r.NotNil(transport.DialContext)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The tunnel dialer reaches an IPv4 loopback server: had the ipv6 pin
	// replaced it, this would fail with the family error instead.
	conn, err := transport.DialContext(ctx, "tcp", server.Listener.Addr().String())
	r.NoError(err)
	r.NoError(conn.Close())
	r.True(tunnelUsed, "the tunnel dialer must still be the one dialing")
}

// TestFamilyDialContext_CatalogedError proves the pinned-family HTTP path fails
// with checkerdef's cataloged error (naming the host and the family) rather
// than the stdlib's opaque "no suitable address found".
func TestFamilyDialContext_CatalogedError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	dial := familyDialContext(checkerdef.IPVersionIPv6)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := dial(ctx, "tcp", "127.0.0.1:80")
	r.Error(err)
	r.ErrorIs(err, checkerdef.ErrNoAddressForFamily)
	r.Contains(err.Error(), "127.0.0.1")
	r.Contains(err.Error(), "IPv6")

	// Positive control: the matching family dials for real.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	conn, err := familyDialContext(checkerdef.IPVersionIPv4)(ctx, "tcp", server.Listener.Addr().String())
	r.NoError(err)
	r.NoError(conn.Close())
}

// TestExecute_ReportsIPVersion proves the resolved family reaches the result
// output for HTTP too, which is how a user verifies an IPv6 check really ran
// over IPv6.
func TestExecute_ReportsIPVersion(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &HTTPConfig{URL: server.URL}
	checker := &HTTPChecker{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	// httptest listens on IPv4 loopback.
	r.Equal("ipv4", result.Output[checkerdef.OutputKeyIPVersion])
}

// TestExecute_PinnedFamilyUnavailable checks the end-to-end HTTP failure: an
// ipv6 pin against an IPv4-only target surfaces the cataloged message, and is
// reported as down (the target really is unreachable over IPv6) rather than as
// an internal error.
func TestExecute_PinnedFamilyUnavailable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &HTTPConfig{URL: server.URL}
	checker := &HTTPChecker{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ctx = checkerdef.WithIPVersion(ctx, checkerdef.IPVersionIPv6)

	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)

	message, _ := result.Output[checkerdef.OutputKeyError].(string)
	r.Contains(message, "no address of the requested IP version")
	r.Contains(message, "IPv6")
}
