package egress

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnforcingDialConnectsToThePinnedAddress is the end-to-end proof of the
// pinned dial: an ENFORCING guard resolves a name that exists in no DNS
// ("pinned.invalid") through its own resolver, drops the refused answer, and
// really connects to the remaining one — the listener sees the connection,
// and the socket's peer is the address the guard resolved, not something the
// stdlib looked up again.
//
// Both answers are loopback (a test cannot reach a public address), so the
// classifier is swapped for one that treats 127.0.0.2 as non-public and
// 127.0.0.1 as public. Everything else is the production path.
func TestEnforcingDialConnectsToThePinnedAddress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan string, 1)

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}

		accepted <- conn.LocalAddr().String()

		_ = conn.Close()
	}()

	lookups := 0

	guard := New(false, WithLookup(func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups++

		r.Equal("pinned.invalid", host)

		return []net.IPAddr{{IP: net.ParseIP("127.0.0.2")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}))
	guard.classify = func(ip net.IP) bool { return ip.Equal(net.ParseIP("127.0.0.2")) }

	_, port, err := net.SplitHostPort(ln.Addr().String())
	r.NoError(err)

	conn, err := guard.DialContext(t.Context(), "tcp", net.JoinHostPort("pinned.invalid", port))
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	r.Equal(net.JoinHostPort("127.0.0.1", port), conn.RemoteAddr().String())
	r.Equal(ln.Addr().String(), <-accepted)
	r.Equal(1, lookups, "the name is resolved exactly once")

	// Positive control: the same guard refuses when only the "private"
	// answer is left.
	guard.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.2")}}, nil
	}

	_, err = guard.DialContext(t.Context(), "tcp", net.JoinHostPort("pinned.invalid", port))
	r.ErrorIs(err, ErrDenied)
}
