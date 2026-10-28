package checkicmp

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"golang.org/x/net/icmp"
)

// DocsURL is where the ICMP permission requirements are explained.
const DocsURL = "https://solidping.io/docs/features/check-types#icmp"

const (
	anyIPv4 = "0.0.0.0"
	anyIPv6 = "::"
)

// errNoICMPSocket marks a host that cannot open any ICMP socket.
var errNoICMPSocket = errors.New("this worker cannot open an ICMP socket")

// noSocketError wraps the raw Go error with an actionable message.
func noSocketError(cause error) error {
	return fmt.Errorf("%w (missing CAP_NET_RAW or net.ipv4.ping_group_range, see %s): %w",
		errNoICMPSocket, DocsURL, cause)
}

// IsNoSocket reports whether err is the "cannot open an ICMP socket" failure.
func IsNoSocket(err error) bool { return errors.Is(err, errNoICMPSocket) }

// listenPacket is the injectable socket opener of the capability probe.
type listenPacket func(network, address string) (net.PacketConn, error)

type probeCandidate struct{ network, address string }

//nolint:gochecknoglobals // constant table
var probeCandidates = []probeCandidate{
	{"udp4", anyIPv4}, {"ip4:icmp", anyIPv4}, {"udp6", anyIPv6}, {"ip6:ipv6-icmp", anyIPv6},
}

// ProbeICMP reports whether a socket of either kind (unprivileged datagram or
// raw), IPv4 or IPv6, can be opened and closed. It never sends a packet.
func ProbeICMP(listen listenPacket) bool {
	for i := range probeCandidates {
		conn, err := listen(probeCandidates[i].network, probeCandidates[i].address)
		if err == nil {
			_ = conn.Close()

			return true
		}
	}

	return false
}

//nolint:gochecknoglobals // process-lifetime probe: sockets do not appear at runtime
var (
	probeOnce   sync.Once
	probeResult bool
)

// Available probes once per process (at first report) whether this worker can
// ping. It is the value behind the `icmp` worker capability.
func Available() bool {
	probeOnce.Do(func() {
		probeResult = ProbeICMP(func(network, address string) (net.PacketConn, error) {
			conn, err := icmp.ListenPacket(network, address)
			if err != nil {
				return nil, err //nolint:wrapcheck // probe only tests success
			}

			return conn, nil
		})
	})

	return probeResult
}
