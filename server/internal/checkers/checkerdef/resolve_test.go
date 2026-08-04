package checkerdef

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func timeoutDNSError(host string) *net.DNSError {
	return &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true, IsTemporary: true}
}

func TestLookupIPAddrSuccessFirstAttempt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	want := []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}
	calls := 0

	addrs, err := lookupIPAddr(context.Background(), "example.com",
		func(ctx context.Context, host string) ([]net.IPAddr, error) {
			calls++

			r.Equal("example.com", host)

			deadline, ok := ctx.Deadline()
			r.True(ok, "each attempt must carry a deadline")
			r.LessOrEqual(time.Until(deadline), resolveAttemptTimeout)

			return want, nil
		})

	r.NoError(err)
	r.Equal(want, addrs)
	r.Equal(1, calls)
}

func TestLookupIPAddrRetriesTransientTimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	want := []net.IPAddr{{IP: net.ParseIP("192.0.2.2")}}
	calls := 0

	addrs, err := lookupIPAddr(context.Background(), "example.com",
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			calls++
			if calls == 1 {
				return nil, timeoutDNSError(host)
			}

			return want, nil
		})

	r.NoError(err)
	r.Equal(want, addrs)
	r.Equal(2, calls)
}

func TestLookupIPAddrDoesNotRetryNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	calls := 0
	notFound := &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}

	addrs, err := lookupIPAddr(context.Background(), "nope.invalid",
		func(_ context.Context, _ string) ([]net.IPAddr, error) {
			calls++

			return nil, notFound
		})

	r.Nil(addrs)
	r.ErrorIs(err, notFound)
	r.Equal(1, calls)
}

func TestLookupIPAddrDoesNotRetryNonDNSError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	calls := 0

	_, err := lookupIPAddr(context.Background(), "example.com",
		func(_ context.Context, _ string) ([]net.IPAddr, error) {
			calls++

			return nil, errBoom
		})

	r.ErrorIs(err, errBoom)
	r.Equal(1, calls)
}

func TestLookupIPAddrGivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	calls := 0

	addrs, err := lookupIPAddr(context.Background(), "example.com",
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			calls++

			return nil, timeoutDNSError(host)
		})

	r.Nil(addrs)

	var dnsErr *net.DNSError

	r.ErrorAs(err, &dnsErr)
	r.True(dnsErr.IsTimeout)
	r.Equal(resolveMaxAttempts, calls)
}

func TestLookupIPAddrStopsWhenParentContextExpires(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	_, err := lookupIPAddr(ctx, "example.com",
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			calls++

			cancel() // parent dies during the first attempt

			return nil, timeoutDNSError(host)
		})

	r.Error(err)
	r.Equal(1, calls, "no retry once the parent context is done")
}

func TestLookupIPAddrIPLiteralNoDNS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// The public entry point with a literal IP must succeed without any DNS
	// traffic (stdlib parses it before consulting the network).
	addrs, err := LookupIPAddr(context.Background(), "127.0.0.1")

	r.NoError(err)
	r.Len(addrs, 1)
	r.True(addrs[0].IP.Equal(net.ParseIP("127.0.0.1")))
}

func TestIsTransientResolveError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isTransientResolveError(timeoutDNSError("h")))
	r.True(isTransientResolveError(&net.DNSError{Err: "server misbehaving", IsTemporary: true}))
	r.False(isTransientResolveError(&net.DNSError{Err: "no such host", IsNotFound: true}))
	// A not-found flagged as timeout by a broken path must still be definitive.
	r.False(isTransientResolveError(&net.DNSError{Err: "x", IsNotFound: true, IsTimeout: true}))
	r.False(isTransientResolveError(&net.DNSError{Err: "lame referral"}))
	r.False(isTransientResolveError(errBoom))
	r.False(isTransientResolveError(context.Canceled))
}
