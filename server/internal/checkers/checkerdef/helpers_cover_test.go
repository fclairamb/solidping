package checkerdef

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

var (
	errRefused     = errors.New("dial: connection refused")
	errNetUnreach  = errors.New("network is unreachable")
	errNoRoute     = errors.New("no route to host")
	errIOTimeout   = errors.New("i/o timeout")
	errUnclassable = errors.New("weird")
)

func TestSmallFormatters(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for format, want := range map[ImageFormat][2]string{
		ImageFormatPNG:  {"image/png", ".png"},
		ImageFormatJPEG: {"image/jpeg", ".jpg"},
		ImageFormatWebP: {"image/webp", ".webp"},
		"gif":           {"", ""},
	} {
		r.Equal(want[0], format.MIME())
		r.Equal(want[1], format.Extension())
	}

	r.Equal("TLS 1.0", TLSVersionString(tls.VersionTLS10))
	r.Equal("TLS 1.1", TLSVersionString(tls.VersionTLS11))
	r.Equal("TLS 1.2", TLSVersionString(tls.VersionTLS12))
	r.Equal("TLS 1.3", TLSVersionString(tls.VersionTLS13))
	r.Contains(TLSVersionString(0x9999), "Unknown")

	r.Equal(`a\\\r\n\tb\x00`, EscapeBytes([]byte("a\\\r\n\tb\x00")))

	r.True(IPVersion("").Valid())
	r.True(IPVersionIPv6.Valid())
	r.False(IPVersion("ipv5").Valid())

	r.Empty(classifyErrorText(nil))
	r.Equal(NetFailureConnectionRefused, classifyErrorText(errRefused))
	r.Equal(NetFailureNetworkUnreachable, classifyErrorText(errNetUnreach))
	r.Equal(NetFailureHostUnreachable, classifyErrorText(errNoRoute))
	r.Equal(NetFailureConnectTimeout, classifyErrorText(errIOTimeout))
	r.Empty(classifyErrorText(errUnclassable))
}

func TestResolveGuarded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	strict := egress.New(false)

	// Unpinned, no guard: a literal and a resolvable name.
	ip, err := resolveGuarded(ctx, "127.0.0.1", IPVersionAuto, nil)
	r.NoError(err)
	r.Equal("127.0.0.1", ip.String())
	ip, err = resolveGuarded(ctx, "localhost", IPVersionAuto, nil)
	r.NoError(err)
	r.True(ip.IsLoopback())
	_, err = resolveGuarded(ctx, "does-not-exist.invalid", IPVersionAuto, nil)
	r.Error(err)

	// Unpinned under an enforcing guard: a loopback is refused.
	_, err = resolveGuarded(ctx, "127.0.0.1", IPVersionAuto, strict)
	r.Error(err)

	// Pinned family.
	ip, err = resolveGuarded(ctx, "127.0.0.1", IPVersionIPv4, nil)
	r.NoError(err)
	r.Equal("127.0.0.1", ip.String())
	_, err = resolveGuarded(ctx, "127.0.0.1", IPVersionIPv6, nil)
	r.ErrorIs(err, ErrNoAddressForFamily)
	_, err = resolveGuarded(ctx, "127.0.0.1", IPVersionIPv4, strict)
	r.Error(err)
	ip, err = resolveGuarded(ctx, "localhost", IPVersionIPv4, nil)
	r.NoError(err)
	r.True(ip.IsLoopback())
	_, err = resolveGuarded(ctx, "localhost", IPVersionIPv4, strict)
	r.Error(err)
	_, err = resolveGuarded(ctx, "does-not-exist.invalid", IPVersionIPv4, nil)
	r.Error(err)
}

func TestGuardedNetDialer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No guard: the base dialer (or a fresh one) comes back untouched.
	r.NotNil(GuardedNetDialer(context.Background(), nil))
	base := &net.Dialer{}
	r.Same(base, GuardedNetDialer(context.Background(), base))

	// Enforcing guard chained with the caller's own Control hook.
	ctx := egress.WithGuard(context.Background(), egress.New(false))
	called := false
	base.Control = func(string, string, syscall.RawConn) error {
		called = true

		return nil
	}
	guarded := GuardedNetDialer(ctx, base)
	r.NotSame(base, guarded)
	_, err := guarded.DialContext(ctx, "tcp", "127.0.0.1:1")
	r.Error(err)
	r.False(called, "the guard refuses before the caller's hook runs")
}
