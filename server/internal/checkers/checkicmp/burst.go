package checkicmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// readPollInterval caps a single blocking read on the shared socket so context
// cancellation is noticed promptly even while replies are still awaited.
const readPollInterval = 100 * time.Millisecond

// drainGrace bounds the final sweep after the context ends: replies that
// already reached the socket buffer still count as received, anything still
// in flight at that point is a lost packet.
const drainGrace = 25 * time.Millisecond

// icmpEchoHeaderLen is the header of an echo message: type, code, checksum,
// identifier and sequence number. The configured packet size comes on top.
const icmpEchoHeaderLen = 8

// minReadBufferSize fits a reply to a default-sized echo, and the quote a Time
// Exceeded message carries (at most 576 bytes for IPv4, 1280 for IPv6).
const minReadBufferSize = 1500

// ipv4MaxHeaderLen is an IPv4 header with every option slot used: the most a
// platform that leaves the IP header in front of the ICMP message can prepend.
const ipv4MaxHeaderLen = 60

// timeExceededCodeReassembly is the Time Exceeded code (ICMPv4 and ICMPv6
// alike) for a fragment reassembly timeout. Code 0 is the TTL / hop limit
// running out in transit.
const timeExceededCodeReassembly = 1

var (
	// errTTLExceeded fails a packet a router discarded because its TTL (IPv6:
	// hop limit) ran out before it reached the target. It is a lost packet
	// with a known cause, never a reply: the target was not reached.
	errTTLExceeded = errors.New("time to live exceeded in transit")
	// errReassemblyTimeExceeded fails a packet whose fragments never all
	// reached the target, which a large packet_size makes possible.
	errReassemblyTimeExceeded = errors.New("fragment reassembly time exceeded")
	// errTTLUnsupported is a configured TTL on a socket exposing no way to set it.
	errTTLUnsupported = errors.New("socket does not support setting the ttl")
)

// openBurstSocket opens the burst's shared socket with the burst's TTL / hop
// limit applied (0 keeps the OS default). A package-level variable so tests
// can inject a fake responder; production always uses listenICMP.
var openBurstSocket = listenICMP //nolint:gochecknoglobals // test seam

// burstParams is one burst's settings. Execute applies the count, timeout and
// interval defaults; packetSize and ttl keep 0 as "default" all the way down
// and are resolved where they are used.
type burstParams struct {
	count    int
	timeout  time.Duration
	interval time.Duration
	// packetSize is the Echo Request payload in bytes, the ICMP and IP
	// headers excluded (ping -s). 0 means defaultPacketSize.
	packetSize int
	// ttl is the IPv4 TTL / IPv6 hop limit of every request. 0 keeps the OS
	// default.
	ttl int
}

// payloadSize is the echo payload actually sent: packetSize, or the 56-byte
// default when unset.
func (p burstParams) payloadSize() int {
	if p.packetSize == 0 {
		return defaultPacketSize
	}

	return p.packetSize
}

// readBufferSize fits a full Echo Reply to this burst's requests. A buffer
// shorter than the datagram silently truncates it.
func (p burstParams) readBufferSize() int {
	return max(minReadBufferSize, ipv4MaxHeaderLen+icmpEchoHeaderLen+p.payloadSize())
}

// replyMatcher tells this burst's answers apart from everything else the
// shared socket receives. A raw socket sees every ICMP message reaching the
// host: other bursts' replies, and errors about other processes' packets.
type replyMatcher struct {
	target       net.IP
	proto        int
	requestType  icmp.Type
	replyType    icmp.Type
	exceededType icmp.Type
	// useUDP: the kernel may rewrite the ID on an unprivileged socket, so
	// there the sequence number is the primary match. On a raw socket the ID
	// must be ours too.
	useUDP bool
	id     uint16
	// ttl only feeds the Time Exceeded error message.
	ttl int
}

// ownsEcho reports whether an echo header carries this burst's identifier.
func (m *replyMatcher) ownsEcho(echo *icmp.Echo) bool {
	return m.useUDP || uint16(echo.ID) == m.id
}

// quotedRequestSeq returns the sequence number of the Echo Request an ICMP
// error quotes, when that request is one of this burst's: sent to this target,
// an Echo Request, with this burst's identifier. Anything else (another
// destination, another protocol, a quote too short to tell) is not ours.
func (m *replyMatcher) quotedRequestSeq(quote []byte) (int, bool) {
	dst, inner, ok := splitQuotedDatagram(quote, m.proto)
	if !ok || !dst.Equal(m.target) {
		return 0, false
	}

	msg, err := icmp.ParseMessage(m.proto, inner)
	if err != nil || msg.Type != m.requestType {
		return 0, false
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok || !m.ownsEcho(echo) {
		return 0, false
	}

	return echo.Seq, true
}

// splitQuotedDatagram splits the original datagram an ICMP error quotes into
// its destination address and the ICMP message it carried. It returns false
// when the quote is not an IP datagram of the expected family carrying ICMP,
// or is too short to hold the echo header.
func splitQuotedDatagram(quote []byte, proto int) (net.IP, []byte, bool) {
	if len(quote) == 0 {
		return nil, nil, false
	}

	version := int(quote[0] >> 4)

	switch proto {
	case protocolICMP:
		headerLen := int(quote[0]&0x0f) * 4
		if version != ipv4.Version || headerLen < ipv4.HeaderLen ||
			len(quote) < headerLen+icmpEchoHeaderLen || quote[9] != protocolICMP {
			return nil, nil, false
		}

		return net.IP(quote[16:20]), quote[headerLen:], true
	case protocolICMPv6:
		if version != ipv6.Version || len(quote) < ipv6.HeaderLen+icmpEchoHeaderLen || quote[6] != protocolICMPv6 {
			return nil, nil, false
		}

		return net.IP(quote[24:40]), quote[ipv6.HeaderLen:], true
	default:
		return nil, nil, false
	}
}

// timeExceededError is the per-packet error of a request an ICMP Time
// Exceeded reported discarded, naming the router that said so.
func timeExceededError(code int, peer net.Addr, ttl int) error {
	from := "an unknown router"
	if ip := addrIP(peer); ip != nil {
		from = ip.String()
	}

	if code == timeExceededCodeReassembly {
		return fmt.Errorf("%w, reported by %s", errReassemblyTimeExceeded, from)
	}

	if ttl > 0 {
		return fmt.Errorf("%w (ttl %d), reported by %s", errTTLExceeded, ttl, from)
	}

	return fmt.Errorf("%w, reported by %s", errTTLExceeded, from)
}

// addrIP extracts the IP of a ReadFrom peer: *net.UDPAddr on an unprivileged
// socket, *net.IPAddr on a raw one.
func addrIP(addr net.Addr) net.IP {
	switch peer := addr.(type) {
	case *net.IPAddr:
		return peer.IP
	case *net.UDPAddr:
		return peer.IP
	default:
		return nil
	}
}

// burstState tracks the in-flight burst: per-sequence send times, which
// packets were actually written and which have been answered. Guarded by mu;
// the sender and the reader goroutine both touch it.
type burstState struct {
	mu        sync.Mutex
	count     int
	timeout   time.Duration
	sentAt    []time.Time
	written   []bool
	answered  []bool
	results   []pingResult
	sendingOK bool
}

func newBurstState(count int, timeout time.Duration) *burstState {
	return &burstState{
		count:    count,
		timeout:  timeout,
		sentAt:   make([]time.Time, count),
		written:  make([]bool, count),
		answered: make([]bool, count),
		results:  make([]pingResult, count),
	}
}

// markSent records packet i as scheduled for writing; on a failed write the
// caller calls unmarkSent so the packet is excluded from the reported burst.
// The flag is set BEFORE the write: the reply can be sitting in the socket
// buffer the instant WriteTo returns, and a reader that sees the reply but not
// the flag would discard it.
func (s *burstState) markSent(i int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sentAt[i] = time.Now()
	s.written[i] = true
}

func (s *burstState) unmarkSent(i int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.written[i] = false
}

func (s *burstState) setSendingDone() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sendingOK = true
}

func (s *burstState) sendingDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sendingOK
}

// nextDeadline returns the earliest still-live deadline among written-but-
// unanswered packets. Packets whose deadline has already passed are left
// unanswered (they report as lost) and stop extending the wait — a reply that
// lands after its own timeout is not worth waiting for, though one already
// sitting in the socket buffer is still accepted on the next read.
func (s *burstState) nextDeadline() time.Time {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	var next time.Time

	for i := 0; i < s.count; i++ {
		if !s.written[i] || s.answered[i] {
			continue
		}

		deadline := s.sentAt[i].Add(s.timeout)
		if deadline.After(now) && (next.IsZero() || deadline.Before(next)) {
			next = deadline
		}
	}

	return next
}

// acceptReply matches one datagram against the written packets and records it.
// An Echo Reply answers its packet with an RTT. A Time Exceeded quoting one of
// this burst's requests fails that packet: a router discarded it (TTL too low,
// or a routing loop), so no reply will ever come and waiting out its timeout
// would only delay the result. It never counts as a reply. Everything else is
// someone else's traffic.
//
// An unprivileged Linux socket never sees the Time Exceeded (the kernel only
// queues ICMP errors for sockets that opted into IP_RECVERR): there the packet
// simply reports as lost when its timeout expires.
func (s *burstState) acceptReply(data []byte, peer net.Addr, matcher *replyMatcher) {
	msg, parseErr := icmp.ParseMessage(matcher.proto, data)
	if parseErr != nil {
		return
	}

	switch msg.Type {
	case matcher.replyType:
		if echo, ok := msg.Body.(*icmp.Echo); ok && matcher.ownsEcho(echo) {
			s.answer(echo.Seq, nil)
		}
	case matcher.exceededType:
		exceeded, ok := msg.Body.(*icmp.TimeExceeded)
		if !ok {
			return
		}

		if seq, ours := matcher.quotedRequestSeq(exceeded.Data); ours {
			s.answer(seq, timeExceededError(msg.Code, peer, matcher.ttl))
		}
	}
}

// answer records the outcome of written packet seq; the first answer wins. A
// nil failure is an Echo Reply, timed now; a non-nil one is a packet the
// network reported discarded.
func (s *burstState) answer(seq int, failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if seq < 0 || seq >= s.count || !s.written[seq] || s.answered[seq] {
		return
	}

	s.answered[seq] = true

	if failure != nil {
		s.results[seq] = pingResult{Success: false, Error: failure}

		return
	}

	s.results[seq] = pingResult{Success: true, RTT: time.Since(s.sentAt[seq])}
}

// collect compacts the burst: one entry per packet actually written, in
// sequence order — answered packets carry their RTT (or the error a Time
// Exceeded reported), written-but-unanswered packets report a per-packet
// timeout (a real lost packet).
func (s *burstState) collect() []pingResult {
	out := make([]pingResult, 0, s.count)

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := 0; i < s.count; i++ {
		if !s.written[i] {
			continue
		}

		if s.answered[i] {
			out = append(out, s.results[i])
		} else {
			out = append(out, pingResult{Success: false, Error: context.DeadlineExceeded})
		}
	}

	return out
}

// performICMPPings runs one ICMP burst: `count` Echo Requests written on a
// fixed schedule (packet i at i × interval, the first immediately) over a
// single shared socket, with replies collected asynchronously — the fping
// model (spec 2026-09-21-01). Run time is therefore
// (count-1) × interval + timeout, independent of packet loss: one lost packet
// costs only its own per-packet `timeout`, never the whole budget.
//
// It returns one pingResult per packet that was actually written to the
// socket, in sequence order — packets the schedule never reached (context
// done) or whose write failed are omitted, so callers report only what was
// really sent. A non-nil error means the burst could not run at all (socket
// setup failure, or every write failed).
func performICMPPings(ctx context.Context, ip net.IP, isIPv6 bool, params burstParams) ([]pingResult, error) {
	conn, useUDP, proto, listenErr := openBurstSocket(isIPv6, params.ttl)
	if listenErr != nil {
		return nil, listenErr
	}

	defer func() { _ = conn.Close() }()

	matcher := &replyMatcher{
		target:       ip,
		proto:        proto,
		requestType:  ipv4.ICMPTypeEcho,
		replyType:    ipv4.ICMPTypeEchoReply,
		exceededType: ipv4.ICMPTypeTimeExceeded,
		useUDP:       useUDP,
		// Process ID as identifier, masked to 16 bits — same as before; all
		// packets of the burst share it and are told apart by sequence number.
		id:  uint16(os.Getpid() & 0xffff),
		ttl: params.ttl,
	}

	if isIPv6 {
		matcher.requestType = ipv6.ICMPTypeEchoRequest
		matcher.replyType = ipv6.ICMPTypeEchoReply
		matcher.exceededType = ipv6.ICMPTypeTimeExceeded
	}

	var dst net.Addr

	if useUDP {
		dst = &net.UDPAddr{IP: ip}
	} else {
		dst = &net.IPAddr{IP: ip}
	}

	state := newBurstState(params.count, params.timeout)

	// Start the reader BEFORE the first packet goes out (spec 2026-09-21-01):
	// sendBurst blocks this goroutine for the whole sending schedule, so a
	// reader started after it returns would never drain the socket during the
	// burst — replies would pile into the kernel receive buffer, which drops
	// datagrams once full. That is a bulk of "lost" packets plus RTTs inflated
	// to the drain moment, exactly what the per-packet-socket code never saw.
	readerDone := readBurst(ctx, conn, state, matcher, params.readBufferSize())

	writeErr := sendBurst(ctx, conn, dst, state, matcher, params)

	// Wait for the reader: either everything written has been answered or has
	// expired, or the context ended and the reader notices within one poll.
	<-readerDone

	out := state.collect()

	if len(out) == 0 && writeErr == nil {
		writeErr = ctx.Err()
	}

	if len(out) == 0 && writeErr != nil {
		return nil, writeErr
	}

	return out, nil
}

// sendBurst writes the Echo Requests on schedule: packet i at i × interval,
// each carrying the configured payload size. The socket buffer absorbs replies
// between reads, so sending never waits on the reader. Returns the first write
// error, if any packet failed to leave.
func sendBurst(
	ctx context.Context,
	conn net.PacketConn,
	dst net.Addr,
	state *burstState,
	matcher *replyMatcher,
	params burstParams,
) error {
	ticker := time.NewTicker(params.interval)
	defer ticker.Stop()

	// Zero bytes, like ping's default pattern; Marshal copies it, so one
	// buffer serves every packet of the burst.
	payload := make([]byte, params.payloadSize())

	var writeErr error

	for i := 0; i < state.count; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-ticker.C:
			}

			if ctx.Err() != nil {
				break
			}
		}

		msg := &icmp.Message{
			Type: matcher.requestType,
			Code: 0,
			Body: &icmp.Echo{ID: int(matcher.id), Seq: i, Data: payload},
		}

		msgBytes, marshalErr := msg.Marshal(nil)
		if marshalErr != nil {
			continue // packet never written: excluded from the reported burst
		}

		state.markSent(i)

		if _, err := conn.WriteTo(msgBytes, dst); err != nil {
			state.unmarkSent(i) // nothing went out, so no reply can match this seq

			if writeErr == nil {
				writeErr = err
			}
		}
	}

	state.setSendingDone()

	return writeErr
}

// readBurst matches replies to written packets as they arrive, until the
// sender is done and nothing written is left unanswered, or the context ends.
// bufSize must fit a full Echo Reply. Returns a channel closed when the reader
// exits.
func readBurst(
	ctx context.Context,
	conn net.PacketConn,
	state *burstState,
	matcher *replyMatcher,
	bufSize int,
) <-chan struct{} {
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)

		buf := make([]byte, bufSize)

		for {
			if ctx.Err() != nil {
				// The burst is over, but replies that already arrived must
				// still count — this goroutine may only now be getting
				// scheduled after a truncation. One bounded sweep drains
				// whatever the socket buffer holds.
				drainReplies(conn, buf, state, matcher)

				return
			}

			deadline := state.nextDeadline()
			if deadline.IsZero() {
				if state.sendingDone() {
					return
				}

				// The sender has not finished scheduling: keep the socket
				// drained for a short poll and re-check.
				deadline = time.Now().Add(readPollInterval)
			}

			// Cap each wait at readPollInterval so ctx cancellation and
			// freshly-sent packets are re-checked promptly.
			wait := time.Until(deadline)
			if wait > readPollInterval {
				wait = readPollInterval
			}

			if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
				return
			}

			bytesRead, peer, readErr := conn.ReadFrom(buf)

			if readErr != nil {
				continue // deadline (or transient error): loop re-checks pending state
			}

			state.acceptReply(buf[:bytesRead], peer, matcher)
		}
	}()

	return readerDone
}

// drainReplies sweeps the socket buffer for replies that arrived before the
// context ended.
func drainReplies(conn net.PacketConn, buf []byte, state *burstState, matcher *replyMatcher) {
	for {
		if err := conn.SetReadDeadline(time.Now().Add(drainGrace)); err != nil {
			return
		}

		bytesRead, peer, readErr := conn.ReadFrom(buf)
		if readErr != nil {
			return
		}

		state.acceptReply(buf[:bytesRead], peer, matcher)
	}
}

// listenICMP opens the burst's shared socket: unprivileged UDP first
// (udp4/udp6), falling back to privileged raw ICMP (ip4:icmp/ip6:ipv6-icmp),
// exactly like the per-packet sockets it replaces, then applies the TTL / hop
// limit (0 keeps the OS default). Returns the connection, whether it is the
// UDP (unprivileged) kind, the ICMP protocol number for reply parsing, and any
// setup error.
func listenICMP(isIPv6 bool, ttl int) (net.PacketConn, bool, int, error) {
	network, rawNetwork, listenAddr, proto := "udp4", "ip4:icmp", anyIPv4, protocolICMP

	if isIPv6 {
		network, rawNetwork, listenAddr, proto = "udp6", "ip6:ipv6-icmp", anyIPv6, protocolICMPv6
	}

	useUDP := true

	conn, err := icmp.ListenPacket(network, listenAddr)
	if err != nil {
		useUDP = false

		conn, err = icmp.ListenPacket(rawNetwork, listenAddr)
		if err != nil {
			return nil, false, 0, noSocketError(err)
		}
	}

	if ttlErr := setOutgoingTTL(conn, isIPv6, ttl); ttlErr != nil {
		_ = conn.Close()

		return nil, false, 0, ttlErr
	}

	return conn, useUDP, proto, nil
}

// ipv4OptionConn and ipv6OptionConn are how *icmp.PacketConn exposes its
// per-family socket options.
type (
	ipv4OptionConn interface{ IPv4PacketConn() *ipv4.PacketConn }
	ipv6OptionConn interface{ IPv6PacketConn() *ipv6.PacketConn }
)

// setOutgoingTTL sets the TTL (IPv4) or unicast hop limit (IPv6) of every
// packet the socket sends. Both socket kinds the burst uses take it: the
// unprivileged datagram ICMP socket and the raw one. 0 leaves the OS default.
func setOutgoingTTL(conn net.PacketConn, isIPv6 bool, ttl int) error {
	if ttl == 0 {
		return nil
	}

	if isIPv6 {
		opts, ok := conn.(ipv6OptionConn)
		if !ok || opts.IPv6PacketConn() == nil {
			return errTTLUnsupported
		}

		if err := opts.IPv6PacketConn().SetHopLimit(ttl); err != nil {
			return fmt.Errorf("setting hop limit %d: %w", ttl, err)
		}

		return nil
	}

	opts, ok := conn.(ipv4OptionConn)
	if !ok || opts.IPv4PacketConn() == nil {
		return errTTLUnsupported
	}

	if err := opts.IPv4PacketConn().SetTTL(ttl); err != nil {
		return fmt.Errorf("setting ttl %d: %w", ttl, err)
	}

	return nil
}
