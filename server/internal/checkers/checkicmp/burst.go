package checkicmp

import (
	"context"
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

// openBurstSocket opens the burst's shared socket. A package-level variable so
// tests can inject a fake responder; production always uses listenICMP.
var openBurstSocket = listenICMP //nolint:gochecknoglobals // test seam

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
func (s *burstState) acceptReply(data []byte, proto int, replyType icmp.Type, useUDP bool, id uint16) {
	replyMsg, parseErr := icmp.ParseMessage(proto, data)
	if parseErr != nil || replyMsg.Type != replyType {
		return
	}

	echo, ok := replyMsg.Body.(*icmp.Echo)
	if !ok || echo.Seq >= s.count {
		return
	}

	// In UDP mode the kernel may rewrite the ID, so there — as before — the
	// sequence number is the primary match; in privileged mode the ID must be
	// ours too.
	if !useUDP && uint16(echo.ID) != id {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.written[echo.Seq] && !s.answered[echo.Seq] {
		s.answered[echo.Seq] = true
		s.results[echo.Seq] = pingResult{Success: true, RTT: time.Since(s.sentAt[echo.Seq])}
	}
}

// collect compacts the burst: one entry per packet actually written, in
// sequence order — answered packets carry their RTT, written-but-unanswered
// packets report a per-packet timeout (a real lost packet).
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
func performICMPPings(
	ctx context.Context,
	ip net.IP,
	isIPv6 bool,
	count int,
	timeout, interval time.Duration,
) ([]pingResult, error) {
	conn, useUDP, proto, listenErr := openBurstSocket(isIPv6)
	if listenErr != nil {
		return nil, listenErr
	}

	defer func() { _ = conn.Close() }()

	requestType, replyType := icmp.Type(ipv4.ICMPTypeEcho), icmp.Type(ipv4.ICMPTypeEchoReply)
	if isIPv6 {
		requestType, replyType = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}

	// Process ID as identifier, masked to 16 bits — same as before; all
	// packets of the burst share it and are told apart by sequence number.
	id := uint16(os.Getpid() & 0xffff)

	var dst net.Addr

	if useUDP {
		dst = &net.UDPAddr{IP: ip}
	} else {
		dst = &net.IPAddr{IP: ip}
	}

	state := newBurstState(count, timeout)

	// Start the reader BEFORE the first packet goes out (spec 2026-09-21-01):
	// sendBurst blocks this goroutine for the whole sending schedule, so a
	// reader started after it returns would never drain the socket during the
	// burst — replies would pile into the kernel receive buffer, which drops
	// datagrams once full. That is a bulk of "lost" packets plus RTTs inflated
	// to the drain moment, exactly what the per-packet-socket code never saw.
	readerDone := readBurst(ctx, conn, state, proto, replyType, useUDP, id)

	writeErr := sendBurst(ctx, conn, dst, state, requestType, id, interval)

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

// sendBurst writes the Echo Requests on schedule: packet i at i × interval.
// The socket buffer absorbs replies between reads, so sending never waits on
// the reader. Returns the first write error, if any packet failed to leave.
func sendBurst(
	ctx context.Context,
	conn net.PacketConn,
	dst net.Addr,
	state *burstState,
	requestType icmp.Type,
	id uint16,
	interval time.Duration,
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

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
			Type: requestType,
			Code: 0,
			Body: &icmp.Echo{ID: int(id), Seq: i, Data: make([]byte, defaultPacketSize)},
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

// readBurst matches Echo Replies to written packets as they arrive, until the
// sender is done and nothing written is left unanswered, or the context ends.
// Returns a channel closed when the reader exits.
func readBurst(
	ctx context.Context,
	conn net.PacketConn,
	state *burstState,
	proto int,
	replyType icmp.Type,
	useUDP bool,
	id uint16,
) <-chan struct{} {
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)

		buf := make([]byte, 1500)

		for {

			if ctx.Err() != nil {
				// The burst is over, but replies that already arrived must
				// still count — this goroutine may only now be getting
				// scheduled after a truncation. One bounded sweep drains
				// whatever the socket buffer holds.
				drainReplies(conn, buf, state, proto, replyType, useUDP, id)

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

			bytesRead, _, readErr := conn.ReadFrom(buf)

			if readErr != nil {
				continue // deadline (or transient error): loop re-checks pending state
			}

			state.acceptReply(buf[:bytesRead], proto, replyType, useUDP, id)
		}
	}()

	return readerDone
}

// drainReplies sweeps the socket buffer for replies that arrived before the
// context ended.
func drainReplies(
	conn net.PacketConn,
	buf []byte,
	state *burstState,
	proto int,
	replyType icmp.Type,
	useUDP bool,
	id uint16,
) {
	for {
		if err := conn.SetReadDeadline(time.Now().Add(drainGrace)); err != nil {
			return
		}

		bytesRead, _, readErr := conn.ReadFrom(buf)
		if readErr != nil {
			return
		}

		state.acceptReply(buf[:bytesRead], proto, replyType, useUDP, id)
	}
}

// listenICMP opens the burst's shared socket: unprivileged UDP first
// (udp4/udp6), falling back to privileged raw ICMP (ip4:icmp/ip6:ipv6-icmp),
// exactly like the per-packet sockets it replaces. Returns the connection,
// whether it is the UDP (unprivileged) kind, the ICMP protocol number for
// reply parsing, and any setup error.
func listenICMP(isIPv6 bool) (net.PacketConn, bool, int, error) {
	network, listenAddr, proto := "udp4", "0.0.0.0", protocolICMP

	if isIPv6 {
		network, listenAddr, proto = "udp6", "::", protocolICMPv6
	}

	conn, err := icmp.ListenPacket(network, listenAddr)
	if err == nil {
		return conn, true, proto, nil
	}

	if isIPv6 {
		network = "ip6:ipv6-icmp"
	} else {
		network = "ip4:icmp"
	}

	conn, err = icmp.ListenPacket(network, listenAddr)
	if err != nil {
		return nil, false, 0, err
	}

	return conn, false, proto, nil
}
