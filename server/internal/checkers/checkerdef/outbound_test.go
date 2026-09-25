package checkerdef_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

func denyCtx(t *testing.T) context.Context {
	t.Helper()

	return egress.WithGuard(t.Context(), egress.New(false))
}

func allowCtx(t *testing.T) context.Context {
	t.Helper()

	return egress.WithGuard(t.Context(), egress.New(true))
}

// A permissive (or absent) guard changes nothing: no dialer is handed out and
// the plain HTTP path keeps http.DefaultTransport.
func TestOutboundSeamIsInertWhenNotEnforcing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, ctx := range []context.Context{t.Context(), allowCtx(t)} {
		r.Nil(checkerdef.OutboundDialer(ctx))
		r.Nil(checkerdef.HTTPTransportFor(ctx, false), "nil transport = DefaultTransport and its pool")
		r.False(checkerdef.EgressEnforcing(ctx))

		base := &net.Dialer{}
		r.Same(base, checkerdef.GuardDialerOr(ctx, base))
		r.Same(base, checkerdef.OutboundDialerOr(ctx, base))
		r.Same(base, checkerdef.GuardedNetDialer(ctx, base))
		r.Same(http.DefaultClient, checkerdef.GuardedHTTPClient(ctx))

		host, err := checkerdef.PinTargetHost(ctx, "localhost")
		r.NoError(err)
		r.Equal("localhost", host, "not enforcing: the name is handed through untouched")
	}
}

func TestOutboundSeamEnforces(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ctx := denyCtx(t)
	r.True(checkerdef.EgressEnforcing(ctx))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)
	t.Cleanup(func() { _ = ln.Close() })

	for name, dialer := range map[string]checkerdef.ContextDialer{
		"OutboundDialer":   checkerdef.OutboundDialer(ctx),
		"OutboundDialerOr": checkerdef.OutboundDialerOr(ctx, &net.Dialer{}),
		"GuardDialerOr":    checkerdef.GuardDialerOr(ctx, &net.Dialer{}),
		"GuardedNetDialer": checkerdef.GuardedNetDialer(ctx, &net.Dialer{}),
	} {
		r.NotNil(dialer, name)

		_, dialErr := dialer.DialContext(ctx, "tcp", ln.Addr().String())
		r.ErrorIs(dialErr, egress.ErrDenied, name)
	}

	_, err = checkerdef.PinTargetHost(ctx, "127.0.0.1")
	r.ErrorIs(err, egress.ErrDenied)

	r.ErrorIs(checkerdef.CheckEgressIP(ctx, "acme", net.ParseIP("169.254.169.254")), egress.ErrDenied)
	r.NoError(checkerdef.CheckEgressIP(ctx, "acme", net.ParseIP("8.8.8.8")))
	r.NotSame(http.DefaultClient, checkerdef.GuardedHTTPClient(ctx))
}

// A tunnel wins over the guard: the tunneled probe leaves from the bastion's
// network, and its name is resolved there.
func TestTunnelDialerWinsOverTheGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	called := false
	tunnel := checkerdef.DialerFunc(func(context.Context, string, string) (net.Conn, error) {
		called = true

		return nil, net.ErrClosed
	})

	ctx := checkerdef.WithTunnelDialer(denyCtx(t), tunnel)

	_, err := checkerdef.OutboundDialer(ctx).DialContext(ctx, "tcp", "10.0.0.1:22")
	r.ErrorIs(err, net.ErrClosed)
	r.True(called)

	transport, ok := checkerdef.HTTPTransportFor(ctx, false).(*http.Transport)
	r.True(ok)
	r.NotNil(transport.DialContext)
}

// The HTTP choke point: under an enforcing guard the transport refuses a
// loopback target, whatever the TLS / family knobs, and a successful
// permissive request is the positive control.
func TestHTTPTransportForRefusesLoopback(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("internal"))
	}))
	t.Cleanup(server.Close)

	get := func(ctx context.Context, skipTLS bool) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		r.NoError(err)

		resp, err := (&http.Client{Transport: checkerdef.HTTPTransportFor(ctx, skipTLS)}).Do(req)
		if err != nil {
			return err
		}

		return resp.Body.Close()
	}

	deny := denyCtx(t)
	r.ErrorIs(get(deny, false), egress.ErrDenied, "shared guarded transport")
	r.ErrorIs(get(deny, true), egress.ErrDenied, "private transport (verifySsl: false)")
	r.ErrorIs(get(checkerdef.WithIPVersion(deny, checkerdef.IPVersionIPv4), false), egress.ErrDenied,
		"pinned family: the selected address is judged before it is dialed")

	r.NoError(get(allowCtx(t), false))
	r.NoError(get(checkerdef.WithIPVersion(allowCtx(t), checkerdef.IPVersionIPv4), false))
}

// A refusal reads the same on every check type: Error, never Down.
func TestResolveFailureStatusForEgressDenial(t *testing.T) {
	t.Parallel()

	err := egress.New(false).CheckIP(net.ParseIP("127.0.0.1"))
	require.Equal(t, checkerdef.StatusError, checkerdef.ResolveFailureStatus(err, checkerdef.StatusDown))
	require.Equal(t, checkerdef.StatusError, checkerdef.IPVersionFailureStatus(err))
}
