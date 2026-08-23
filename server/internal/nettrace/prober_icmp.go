package nettrace

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// icmpProber implements the two ICMP echo modes. They differ ONLY in which
// socket they open: a raw one (needs root/CAP_NET_RAW) or the unprivileged
// datagram one. Everything after the listen — the echo, the TTL, the reply
// matching — is identical, which is why one type serves both.
type icmpProber struct {
	conn *icmp.PacketConn
	mode Mode
	// ipv6 selects the protocol numbers, message types and the TTL/hop-limit
	// setter. There is no dual-stack prober: a trace follows the family the
	// failing check pinned.
	isV6 bool
	// id is this prober's echo identifier. On a DATAGRAM socket the kernel
	// rewrites it, which is exactly why reply matching keys on the sequence
	// number instead — see matchesSeq.
	id int
}

// icmpPayload is the echo body. Small and constant: the useful identity lives
// in the header, and the 8 bytes an ICMP error quotes back never include the
// payload anyway.
//
//nolint:gochecknoglobals // immutable byte constant
var icmpPayload = []byte("solidping-trace")

// newICMPProber opens the socket for one mode/family pair.
func newICMPProber(mode Mode, isV6 bool) (*icmpProber, error) {
	network, address := icmpListenSpec(mode, isV6)

	conn, err := icmp.ListenPacket(network, address)
	if err != nil {
		return nil, fmt.Errorf("open %s socket: %w", network, err)
	}

	return &icmpProber{conn: conn, mode: mode, isV6: isV6, id: os.Getpid() & 0xffff}, nil
}

// icmpListenSpec maps a mode/family to the (network, address) icmp.ListenPacket
// wants. `ip4:icmp` and `ip6:ipv6-icmp` are RAW; `udp4`/`udp6` select the
// SOCK_DGRAM ICMP socket, which is the unprivileged tier.
func icmpListenSpec(mode Mode, isV6 bool) (string, string) {
	switch {
	case mode == ModeICMPRaw && isV6:
		return "ip6:ipv6-icmp", "::"
	case mode == ModeICMPRaw:
		return "ip4:icmp", "0.0.0.0"
	case isV6:
		return "udp6", "::"
	default:
		return "udp4", "0.0.0.0"
	}
}

// Mode implements Prober.
func (p *icmpProber) Mode() Mode { return p.mode }

// Close implements Prober.
func (p *icmpProber) Close() error {
	if p.conn == nil {
		return nil
	}

	err := p.conn.Close()
	p.conn = nil

	if err != nil {
		return fmt.Errorf("close icmp socket: %w", err)
	}

	return nil
}

// Probe sends one TTL-limited echo and waits for the matching reply.
func (p *icmpProber) Probe(ctx context.Context, dst net.IP, ttl, seq int) (Reply, error) {
	if err := p.setTTL(ttl); err != nil {
		return Reply{}, err
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultProbeTimeout)
	}

	if err := p.conn.SetDeadline(deadline); err != nil {
		return Reply{}, fmt.Errorf("set icmp deadline: %w", err)
	}

	body, err := p.echoBytes(seq)
	if err != nil {
		return Reply{}, err
	}

	start := time.Now()

	if _, err := p.conn.WriteTo(body, p.destination(dst)); err != nil {
		return Reply{}, fmt.Errorf("send echo: %w", err)
	}

	return p.awaitReply(ctx, dst, seq, start)
}

// destination wraps the target for the socket type. A raw socket addresses an
// *net.IPAddr; the datagram socket addresses a *net.UDPAddr with port 0 (the
// kernel supplies the ICMP identifier).
func (p *icmpProber) destination(dst net.IP) net.Addr {
	if p.mode == ModeICMPRaw {
		return &net.IPAddr{IP: dst}
	}

	return &net.UDPAddr{IP: dst}
}

func (p *icmpProber) setTTL(ttl int) error {
	if p.isV6 {
		if err := p.conn.IPv6PacketConn().SetHopLimit(ttl); err != nil {
			return fmt.Errorf("set hop limit: %w", err)
		}

		return nil
	}

	if err := p.conn.IPv4PacketConn().SetTTL(ttl); err != nil {
		return fmt.Errorf("set ttl: %w", err)
	}

	return nil
}

func (p *icmpProber) echoBytes(seq int) ([]byte, error) {
	msgType := icmp.Type(ipv4.ICMPTypeEcho)
	if p.isV6 {
		msgType = ipv6.ICMPTypeEchoRequest
	}

	message := icmp.Message{
		Type: msgType,
		Code: 0,
		Body: &icmp.Echo{ID: p.id, Seq: seq, Data: icmpPayload},
	}

	body, err := message.Marshal(nil)
	if err != nil {
		return nil, fmt.Errorf("marshal echo: %w", err)
	}

	return body, nil
}

// awaitReply drains the socket until it sees a reply that belongs to this
// probe, or the deadline passes.
//
// Draining rather than trusting the first packet is not optional: an ICMP
// socket sees every echo reply and every ICMP error the host receives, so a
// concurrent ping elsewhere on the machine would otherwise be read as this
// probe's answer and attributed to the wrong hop.
func (p *icmpProber) awaitReply(ctx context.Context, dst net.IP, seq int, start time.Time) (Reply, error) {
	buf := make([]byte, 1500)

	for {
		if ctx.Err() != nil {
			return Reply{}, ErrProbeTimeout
		}

		count, peer, err := p.conn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return Reply{}, ErrProbeTimeout
			}

			return Reply{}, fmt.Errorf("read icmp: %w", err)
		}

		reply, matched := p.interpret(buf[:count], peer, dst, seq, time.Since(start))
		if matched {
			return reply, nil
		}
	}
}

// interpret decodes one received packet and reports whether it answers `seq`.
func (p *icmpProber) interpret(
	packet []byte, peer net.Addr, dst net.IP, seq int, rtt time.Duration,
) (Reply, bool) {
	proto := ipv4.ICMPTypeEchoReply.Protocol()
	if p.isV6 {
		proto = ipv6.ICMPTypeEchoReply.Protocol()
	}

	message, err := icmp.ParseMessage(proto, packet)
	if err != nil {
		return Reply{}, false
	}

	from := addrIP(peer)

	switch body := message.Body.(type) {
	case *icmp.Echo:
		// An echo REPLY from the target. On the datagram socket the kernel
		// rewrote our id, so the sequence number is the only reliable match.
		if body.Seq != seq {
			return Reply{}, false
		}

		return Reply{From: from, RTT: rtt, Final: from == nil || from.Equal(dst)}, true

	case *icmp.TimeExceeded:
		if !quotedSeqMatches(body.Data, p.isV6, seq) {
			return Reply{}, false
		}

		return Reply{From: from, RTT: rtt}, true

	case *icmp.DstUnreach:
		if !quotedSeqMatches(body.Data, p.isV6, seq) {
			return Reply{}, false
		}

		return Reply{From: from, RTT: rtt, Unreachable: true}, true

	default:
		return Reply{}, false
	}
}

// quotedSeqMatches reads the sequence number out of the original datagram an
// ICMP error quotes back.
//
// An ICMP error carries the offending IP header plus its first 8 bytes — which,
// for an echo, is exactly the ICMP header: type, code, checksum, id, seq. The
// id half is unusable on a datagram socket (the kernel replaced ours on the way
// out and the router quotes what it saw), so the sequence number is the whole
// identity we get. It is enough: Trace probes serially and never reuses a seq.
func quotedSeqMatches(quoted []byte, isV6 bool, seq int) bool {
	inner := stripIPHeader(quoted, isV6)
	if len(inner) < 8 {
		return false
	}

	return int(binary.BigEndian.Uint16(inner[6:8])) == seq
}

// stripIPHeader removes the quoted IP header. IPv4 headers are variable length
// (IHL, in 32-bit words); IPv6's is a fixed 40 bytes, and extension headers are
// not something a traceroute echo produces.
func stripIPHeader(quoted []byte, isV6 bool) []byte {
	if isV6 {
		if len(quoted) < 40 {
			return nil
		}

		return quoted[40:]
	}

	if len(quoted) < 20 {
		return nil
	}

	headerLen := int(quoted[0]&0x0f) * 4
	if headerLen < 20 || len(quoted) < headerLen {
		return nil
	}

	return quoted[headerLen:]
}

// addrIP extracts the IP from whichever address type the socket reports.
func addrIP(addr net.Addr) net.IP {
	switch typed := addr.(type) {
	case *net.IPAddr:
		return typed.IP
	case *net.UDPAddr:
		return typed.IP
	default:
		return nil
	}
}
