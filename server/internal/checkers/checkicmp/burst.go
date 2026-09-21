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
	conn, useUDP, proto, listenErr := listenICMP(isIPv6)
	if listenErr != nil {
		return nil, listenErr
	}

	defer func() { _ = conn.Close() }()

	// Protocol shapes, resolved once for the whole burst.
	requestType := icmp.Type(ipv4.ICMPTypeEcho)
	replyType := icmp.Type(ipv4.ICMPTypeEchoReply)

	if isIPv6 {
		requestType = ipv6.ICMPTypeEchoRequest
		replyType = ipv6.ICMPTypeEchoReply
	}

	// Process ID as identifier, masked to 16 bits — same as before; all
	// packets of the burst share it and are told apart by sequence number.
	id := os.Getpid() & 0xffff

	var dst net.Addr

	if useUDP {
		dst = &net.UDPAddr{IP: ip}
	} else {
		dst = &net.IPAddr{IP: ip}
	}

	// Burst state. All three slices are indexed by sequence number; `results`
	// only carries entries for answered packets, the compaction below rebuilds
	// the full per-written-packet view. Everything is guarded by mu because
	// the reader goroutine runs while the sender is still scheduling.
	var mu sync.Mutex

	sentAt := make([]time.Time, count)
	written := make([]bool, count)
	answered := make([]bool, count)
	results := make([]pingResult, count)

	var writeErr error

	// sendingDone flips (under mu) once the sender has finished scheduling —
	// the reader must not interpret "nothing pending yet" as "burst over"
	// while the first packets have not been written.
	sendingDone := false

	// acceptReply matches one datagram against the written packets and records
	// it. Called only from the reader goroutine.
	acceptReply := func(data []byte) {
		replyMsg, parseErr := icmp.ParseMessage(proto, data)
		if parseErr != nil || replyMsg.Type != replyType {
			return
		}

		echo, ok := replyMsg.Body.(*icmp.Echo)
		if !ok || echo.Seq >= count {
			return
		}

		// In UDP mode the kernel may rewrite the ID, so there — as before —
		// the sequence number is the primary match; in privileged mode the
		// ID must be ours too.
		if !useUDP && echo.ID != id {
			return
		}

		mu.Lock()

		if written[echo.Seq] && !answered[echo.Seq] {
			answered[echo.Seq] = true
			results[echo.Seq] = pingResult{Success: true, RTT: time.Since(sentAt[echo.Seq])}
		}

		mu.Unlock()
	}

	// Reader: matches Echo Replies to written packets as they arrive. It exits
	// once the sender is done and nothing written is left unanswered, or the
	// context is done, or every pending packet has passed its own
	// sentAt+timeout deadline.
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)

		buf := make([]byte, 1500)

		for {
			if ctx.Err() != nil {
				return
			}

			// Earliest still-live deadline among written-but-unanswered
			// packets. Packets whose deadline has already passed are left
			// unanswered (they report as lost) and stop extending the wait —
			// a reply that lands after its own timeout is not worth waiting
			// for, though one already sitting in the socket buffer is still
			// accepted on the next read.
			now := time.Now()

			mu.Lock()

			var nextDeadline time.Time

			for i := 0; i < count; i++ {
				if !written[i] || answered[i] {
					continue
				}

				deadline := sentAt[i].Add(timeout)
				if deadline.After(now) && (nextDeadline.IsZero() || deadline.Before(nextDeadline)) {
					nextDeadline = deadline
				}
			}

			done := sendingDone

			mu.Unlock()

			if nextDeadline.IsZero() {
				if done {
					return
				}

				// The sender has not finished scheduling: keep the socket
				// drained for a short poll and re-check.
				nextDeadline = now.Add(readPollInterval)
			}

			// Cap each wait at readPollInterval so ctx cancellation and
			// freshly-sent packets are re-checked promptly.
			wait := nextDeadline.Sub(now)
			if wait > readPollInterval {
				wait = readPollInterval
			}

			if err := conn.SetReadDeadline(now.Add(wait)); err != nil {
				return
			}

			bytesRead, _, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				continue // deadline (or transient error): loop re-checks pending state
			}

			acceptReply(buf[:bytesRead])
		}
	}()

	// Sender: packet i leaves at i × interval. The socket buffer absorbs
	// replies between reads, so sending never waits on the reader.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for i := 0; i < count; i++ {
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
			Body: &icmp.Echo{
				ID:   id,
				Seq:  i,
				Data: make([]byte, defaultPacketSize),
			},
		}

		msgBytes, marshalErr := msg.Marshal(nil)
		if marshalErr != nil {
			continue // packet never written: excluded from the reported burst
		}

		mu.Lock()
		sentAt[i] = time.Now()
		mu.Unlock()

		if _, err := conn.WriteTo(msgBytes, dst); err != nil {
			mu.Lock()

			if writeErr == nil {
				writeErr = err
			}

			mu.Unlock()

			continue
		}

		mu.Lock()
		written[i] = true
		mu.Unlock()
	}

	mu.Lock()
	sendingDone = true
	mu.Unlock()

	// Wait for the reader: either everything written has been answered or has
	// expired, or the context ended and the reader notices within one poll.
	select {
	case <-readerDone:
	case <-ctx.Done():
		<-readerDone
	}

	// Compact: one entry per packet actually written, in sequence order.
	out := make([]pingResult, 0, count)

	for i := 0; i < count; i++ {
		if !written[i] {
			continue
		}

		if answered[i] {
			out = append(out, results[i])
		} else {
			// Sent but never answered: a real lost packet, classified as a
			// per-packet timeout exactly like the sequential loop reported it.
			out = append(out, pingResult{Success: false, Error: context.DeadlineExceeded})
		}
	}

	if len(out) == 0 {
		mu.Lock()
		fatal := writeErr
		mu.Unlock()

		if fatal == nil {
			fatal = ctx.Err()
		}

		if fatal != nil {
			return nil, fatal
		}
	}

	return out, nil
}

// listenICMP opens the burst's shared socket: unprivileged UDP first
// (udp4/udp6), falling back to privileged raw ICMP (ip4:icmp/ip6:ipv6-icmp),
// exactly like the per-packet sockets it replaces. Returns the connection,
// whether it is the UDP (unprivileged) kind, the ICMP protocol number for
// reply parsing, and any setup error.
func listenICMP(isIPv6 bool) (*icmp.PacketConn, bool, int, error) {
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
