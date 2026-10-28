package checkicmp

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

var errDenied = errors.New("socket: operation not permitted")

type closeRecorder struct {
	net.PacketConn
	closed *int
}

func (c closeRecorder) Close() error { *c.closed++; return nil }

func TestProbeICMPNothingOpens(t *testing.T) {
	t.Parallel()

	var tried []string

	got := ProbeICMP(func(network, _ string) (net.PacketConn, error) {
		tried = append(tried, network)

		return nil, errDenied
	})

	require := require.New(t)
	require.False(got)
	require.Equal([]string{"udp4", "ip4:icmp", "udp6", "ip6:ipv6-icmp"}, tried, "both kinds, both families")
}

func TestProbeICMPRawOnlyWorksAndSocketIsClosed(t *testing.T) {
	t.Parallel()

	closed := 0

	got := ProbeICMP(func(network, _ string) (net.PacketConn, error) {
		if network != "ip4:icmp" {
			return nil, errDenied
		}

		return closeRecorder{closed: &closed}, nil
	})

	require.True(t, got)
	require.Equal(t, 1, closed)
}

func TestNoSocketErrorIsActionable(t *testing.T) {
	t.Parallel()

	err := noSocketError(errDenied)

	require.True(t, IsNoSocket(err))
	require.ErrorIs(t, err, errDenied)
	require.Contains(t, err.Error(), "CAP_NET_RAW")
	require.Contains(t, err.Error(), DocsURL)
}
