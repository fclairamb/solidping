package checkicmp

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
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

// A worker that cannot open any ICMP socket reports an infrastructure error,
// not a DOWN target.
func TestExecuteNoSocketIsInfrastructureError(t *testing.T) {
	t.Parallel()

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	previous := openBurstSocket
	openBurstSocket = func(bool, int) (net.PacketConn, bool, int, error) {
		return nil, false, 0, noSocketError(errDenied)
	}

	defer func() { openBurstSocket = previous }()

	result, err := (&ICMPChecker{}).Execute(t.Context(), &ICMPConfig{Host: "127.0.0.1", Count: 1})
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusError, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "ping_group_range")
}
