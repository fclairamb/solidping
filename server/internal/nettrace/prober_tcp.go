package nettrace

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"
)

// ErrNoTargetPort means a TCP-mode prober was asked for without a port to
// probe. An ICMP check has none, which is precisely the case where no trace is
// possible at all on an unprivileged host.
var ErrNoTargetPort = errors.New("nettrace: tcp mode needs a target port")

// tcpProber is the unprivileged last resort: ordinary connects with the TTL
// lowered on the socket.
//
// READ THE HONESTY NOTE ON ModeTCP BEFORE USING WHAT THIS PRODUCES. connect()
// surfaces an errno, never the ICMP source address of the router that produced
// it, so this prober can only answer "did the SYN get far enough to be
// answered at this TTL?". What it yields is the distance to the target and the
// TTL at which the path stops working — useful, and not a hop list.
//
// The compensation is that it probes the check's OWN port, so it crosses the
// same firewalls the check does. A path that passes ICMP and blocks 443 looks
// perfect in the other two modes and broken in this one, and this one is right.
type tcpProber struct {
	port int
	isV6 bool
}

func newTCPProber(port int, isV6 bool) (*tcpProber, error) {
	if port <= 0 {
		return nil, ErrNoTargetPort
	}

	return &tcpProber{port: port, isV6: isV6}, nil
}

// Mode implements Prober.
func (p *tcpProber) Mode() Mode { return ModeTCP }

// Close implements Prober. Nothing is held open between probes.
func (p *tcpProber) Close() error { return nil }

// Probe connects with the TTL pinned, and reads the outcome.
func (p *tcpProber) Probe(ctx context.Context, dst net.IP, ttl, _ int) (Reply, error) {
	dialer := net.Dialer{Control: ttlControl(ttl, p.isV6)}

	network := "tcp4"
	if p.isV6 {
		network = "tcp6"
	}

	start := time.Now()

	conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(dst.String(), strconv.Itoa(p.port)))
	rtt := time.Since(start)

	if err == nil {
		_ = conn.Close()

		// The handshake completed, so the SYN reached the target: this TTL is
		// the target's distance.
		return Reply{From: dst, RTT: rtt, Final: true}, nil
	}

	return classifyTCPProbeError(err, dst, rtt)
}

// classifyTCPProbeError turns a failed connect into a hop verdict.
func classifyTCPProbeError(err error, dst net.IP, rtt time.Duration) (Reply, error) {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		// An RST comes from the TARGET's stack, not from a router: the packet
		// arrived. For a trace that is the same information a completed
		// handshake gives — and for a `connection-refused` check it is the
		// expected outcome, so treating it as silence would make the trace stop
		// exactly where it should have finished.
		return Reply{From: dst, RTT: rtt, Final: true}, nil

	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		// A router rejected the route. We cannot see WHICH one — that is the
		// mode's documented blind spot — but the verdict itself is real.
		return Reply{RTT: rtt, Unreachable: true}, nil

	default:
		// Everything else — the TTL expiring en route included — reaches us as
		// a timeout with no source. Silence.
		return Reply{}, ErrProbeTimeout
	}
}

// ttlControl returns a Dialer.Control that lowers the outgoing TTL before the
// SYN leaves.
//
// It must happen in Control (on the raw fd, pre-connect) rather than on the
// returned net.Conn: by the time a Conn exists the SYN has already gone out at
// the default TTL, which would make every probe reach the target and the whole
// ladder read as "one hop".
func ttlControl(ttl int, isV6 bool) func(network, address string, conn syscall.RawConn) error {
	return func(_, _ string, conn syscall.RawConn) error {
		var sockErr error

		controlErr := conn.Control(func(handle uintptr) {
			if isV6 {
				sockErr = syscall.SetsockoptInt(int(handle), syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, ttl)

				return
			}

			sockErr = syscall.SetsockoptInt(int(handle), syscall.IPPROTO_IP, syscall.IP_TTL, ttl)
		})
		if controlErr != nil {
			return fmt.Errorf("socket control: %w", controlErr)
		}

		if sockErr != nil {
			return fmt.Errorf("set ttl: %w", sockErr)
		}

		return nil
	}
}
