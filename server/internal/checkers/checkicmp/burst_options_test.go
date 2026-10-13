package checkicmp

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// testRouter is the router a fake Time Exceeded comes from.
const testRouter = "192.0.2.254"

// writtenPayloads returns the echo payload size of every request the fake
// conn accepted.
func writtenPayloads(t *testing.T, conn *fakeICMPConn, proto int) []int {
	t.Helper()

	conn.mu.Lock()
	defer conn.mu.Unlock()

	sizes := make([]int, 0, len(conn.writes))

	for _, record := range conn.writes {
		msg, err := icmp.ParseMessage(proto, record.data)
		require.NoError(t, err)

		echo, ok := msg.Body.(*icmp.Echo)
		require.True(t, ok, "request is not an echo: %T", msg.Body)

		sizes = append(sizes, len(echo.Data))
	}

	return sizes
}

// quotedDatagram builds the original datagram an ICMP error quotes: an IP
// header (IPv4 or IPv6) toward dst carrying innerProto, then the first 8
// bytes of the ICMP message (the RFC 792 minimum a router must quote).
func quotedDatagram(isIPv6 bool, dst net.IP, innerProto byte, icmpMsg []byte) []byte {
	inner := icmpMsg[:icmpEchoHeaderLen]

	if isIPv6 {
		datagram := make([]byte, ipv6.HeaderLen+len(inner))
		datagram[0] = ipv6.Version << 4
		datagram[6] = innerProto
		datagram[7] = 1 // hop limit left when it expired
		copy(datagram[24:40], dst.To16())
		copy(datagram[ipv6.HeaderLen:], inner)

		return datagram
	}

	datagram := make([]byte, ipv4.HeaderLen+len(inner))
	datagram[0] = ipv4.Version<<4 | ipv4.HeaderLen/4
	datagram[8] = 1 // TTL left when it expired
	datagram[9] = innerProto
	copy(datagram[16:20], dst.To4())
	copy(datagram[ipv4.HeaderLen:], inner)

	return datagram
}

// timeExceeded builds a Time Exceeded message of the given ICMP protocol and
// code carrying quote.
func timeExceeded(t *testing.T, proto, code int, quote []byte) []byte {
	t.Helper()

	var msgType icmp.Type = ipv4.ICMPTypeTimeExceeded
	if proto == protocolICMPv6 {
		msgType = ipv6.ICMPTypeTimeExceeded
	}

	out, err := (&icmp.Message{Type: msgType, Code: code, Body: &icmp.TimeExceeded{Data: quote}}).Marshal(nil)
	require.NoError(t, err)

	return out
}

// echoMessage marshals an echo message of the given type.
func echoMessage(t *testing.T, msgType icmp.Type, id, seq int) []byte {
	t.Helper()

	out, err := (&icmp.Message{
		Type: msgType,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: make([]byte, defaultPacketSize)},
	}).Marshal(nil)
	require.NoError(t, err)

	return out
}

// timeExceededFor answers an ICMPv4 Echo Request to target the way a router
// whose TTL ran out does.
func timeExceededFor(t *testing.T, target net.IP, req []byte) []byte {
	t.Helper()

	return timeExceeded(t, protocolICMP, 0, quotedDatagram(false, target, protocolICMP, req))
}

// requestSeq returns the sequence number of an ICMPv4 Echo Request, or -1.
func requestSeq(req []byte) int {
	msg, err := icmp.ParseMessage(protocolICMP, req)
	if err != nil {
		return -1
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		return -1
	}

	return echo.Seq
}

// TestPerformICMPPingsSendsConfiguredPacketSize pins the bug this fixes:
// packet_size was validated and then ignored, every request carried 56 bytes.
// Every socket kind the burst opens (unprivileged / raw, IPv4 / IPv6) shares
// the one request builder, and the reader must fit a reply that large.
func TestPerformICMPPingsSendsConfiguredPacketSize(t *testing.T) {
	t.Parallel()

	sockets := []struct {
		name   string
		isIPv6 bool
		useUDP bool
		target net.IP
	}{
		{"ipv4 unprivileged", false, true, net.IPv4(127, 0, 0, 1)},
		{"ipv4 raw", false, false, net.IPv4(127, 0, 0, 1)},
		{"ipv6 unprivileged", true, true, net.IPv6loopback},
		{"ipv6 raw", true, false, net.IPv6loopback},
	}

	sizes := []struct {
		packetSize int
		want       int
	}{
		{0, defaultPacketSize}, // unset: ping's default, not an empty payload
		{1, 1},
		{1000, 1000},
		{9000, 9000}, // larger than the reader's 1500-byte floor
	}

	for _, socket := range sockets {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/%dB", socket.name, size.packetSize), func(t *testing.T) {
				t.Parallel()

				r := require.New(t)

				burstHookMu.Lock()
				defer burstHookMu.Unlock()

				proto := protocolICMP
				if socket.isIPv6 {
					proto = protocolICMPv6
				}

				conn, restore := withFakeSocketMode(socket.useUDP, proto, func(c *fakeICMPConn, req []byte) {
					if reply := echoReplyFor(proto, req); reply != nil {
						c.replies <- reply
					}
				})
				defer restore()

				out, err := performICMPPings(context.Background(), socket.target, socket.isIPv6, burstParams{
					count: 2, timeout: time.Second, interval: 10 * time.Millisecond, packetSize: size.packetSize,
				})
				r.NoError(err)
				r.Len(out, 2)

				for _, res := range out {
					r.True(res.Success, "packet not answered: %v", res.Error)
				}

				r.Equal([]int{size.want, size.want}, writtenPayloads(t, conn, proto))

				conn.mu.Lock()
				minReadBuf := conn.minReadBuf
				conn.mu.Unlock()

				r.GreaterOrEqual(minReadBuf, icmpEchoHeaderLen+size.want, "reader buffer would truncate the reply")
			})
		}
	}
}

// TestExecuteHonoursPacketSizeAndTTL follows packet_size and ttl from the
// check config to what the burst actually uses: the payload written and the
// TTL the socket was opened with. Unset, both keep their defaults.
func TestExecuteHonoursPacketSizeAndTTL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      ICMPConfig
		wantSize int
		wantTTL  int
	}{
		{"unset keeps the defaults", ICMPConfig{}, defaultPacketSize, 0},
		{"configured", ICMPConfig{PacketSize: 300, TTL: 9}, 300, 9},
		{"maximum", ICMPConfig{PacketSize: 65507, TTL: 255}, 65507, 255},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			burstHookMu.Lock()
			defer burstHookMu.Unlock()

			conn, restore := withEchoAll()
			defer restore()

			cfg := tt.cfg
			cfg.Host = "127.0.0.1"
			cfg.Count = 2
			cfg.Interval = 10 * time.Millisecond
			cfg.Timeout = time.Second

			result, err := (&ICMPChecker{}).Execute(context.Background(), &cfg)
			r.NoError(err)
			r.Equal(checkerdef.StatusUp, result.Status)
			r.Equal(2, result.Metrics[metricPacketsReceived])

			r.Equal([]int{tt.wantSize, tt.wantSize}, writtenPayloads(t, conn, protocolICMP))

			conn.mu.Lock()
			openedTTL := conn.openedTTL
			conn.mu.Unlock()

			r.Equal(tt.wantTTL, openedTTL)
		})
	}
}

// udpOptionConn exposes a real UDP socket's IP options the way
// *icmp.PacketConn does, so setOutgoingTTL can be checked against the kernel
// without the privileges an ICMP socket may need.
type udpOptionConn struct {
	net.PacketConn

	p4 *ipv4.PacketConn
	p6 *ipv6.PacketConn
}

func (c *udpOptionConn) IPv4PacketConn() *ipv4.PacketConn { return c.p4 }

func (c *udpOptionConn) IPv6PacketConn() *ipv6.PacketConn { return c.p6 }

// listenUDP opens a plain loopback UDP socket, closed when the test ends.
func listenUDP(t *testing.T, network, address string) (net.PacketConn, error) {
	t.Helper()

	conn, err := (&net.ListenConfig{}).ListenPacket(t.Context(), network, address)
	if err != nil {
		return nil, err
	}

	t.Cleanup(func() { _ = conn.Close() })

	return conn, nil
}

func TestSetOutgoingTTLIPv4(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	udp, err := listenUDP(t, "udp4", "127.0.0.1:0")
	r.NoError(err)

	p4 := ipv4.NewPacketConn(udp)
	conn := &udpOptionConn{PacketConn: udp, p4: p4}

	osDefault, err := p4.TTL()
	r.NoError(err)

	// ttl 0 leaves the socket alone.
	r.NoError(setOutgoingTTL(conn, false, 0))

	got, err := p4.TTL()
	r.NoError(err)
	r.Equal(osDefault, got)

	// A configured ttl reaches the kernel.
	want := 7
	if osDefault == want {
		want = 8
	}

	r.NoError(setOutgoingTTL(conn, false, want))

	got, err = p4.TTL()
	r.NoError(err)
	r.Equal(want, got)
}

func TestSetOutgoingTTLIPv6(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	udp, err := listenUDP(t, "udp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}

	p6 := ipv6.NewPacketConn(udp)
	conn := &udpOptionConn{PacketConn: udp, p6: p6}

	osDefault, err := p6.HopLimit()
	r.NoError(err)

	r.NoError(setOutgoingTTL(conn, true, 0))

	got, err := p6.HopLimit()
	r.NoError(err)
	r.Equal(osDefault, got)

	want := 7
	if osDefault == want {
		want = 8
	}

	r.NoError(setOutgoingTTL(conn, true, want))

	got, err = p6.HopLimit()
	r.NoError(err)
	r.Equal(want, got)
}

// TestSetOutgoingTTLUnsupportedSocket: a configured ttl on a socket that
// cannot take it fails loudly instead of silently probing with the OS
// default; an unset one is fine anywhere.
func TestSetOutgoingTTLUnsupportedSocket(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	conn := newFakeICMPConn(nil)

	r.NoError(setOutgoingTTL(conn, false, 0))
	r.ErrorIs(setOutgoingTTL(conn, false, 5), errTTLUnsupported)
	r.ErrorIs(setOutgoingTTL(conn, true, 5), errTTLUnsupported)

	// An IPv4 socket asked for an IPv6 hop limit is unsupported too.
	udp, err := listenUDP(t, "udp4", "127.0.0.1:0")
	r.NoError(err)

	r.ErrorIs(setOutgoingTTL(&udpOptionConn{PacketConn: udp, p4: ipv4.NewPacketConn(udp)}, true, 5), errTTLUnsupported)
}

// TestListenICMPAppliesTTL runs the production opener. Unprivileged ICMP
// sockets need ping_group_range on Linux (macOS always allows them), raw ones
// need root: skipped when neither is available.
func TestListenICMPAppliesTTL(t *testing.T) {
	t.Parallel()

	for _, isIPv6 := range []bool{false, true} {
		t.Run(fmt.Sprintf("ipv6=%v", isIPv6), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			conn, _, _, err := listenICMP(isIPv6, 7)
			if err != nil {
				t.Skipf("ICMP socket unavailable: %v", err)
			}

			defer func() { _ = conn.Close() }()

			icmpConn, ok := conn.(*icmp.PacketConn)
			r.True(ok)

			var got int

			if isIPv6 {
				got, err = icmpConn.IPv6PacketConn().HopLimit()
			} else {
				got, err = icmpConn.IPv4PacketConn().TTL()
			}

			r.NoError(err)
			r.Equal(7, got)
		})
	}
}

// TestAcceptReplyMatchesTimeExceeded drives the reply parser with every shape
// of Time Exceeded a shared socket can see. Only one quoting this burst's own
// request fails a packet, and never as a success; the rest is someone else's
// and must leave the packet waiting for its own reply.
func TestAcceptReplyMatchesTimeExceeded(t *testing.T) {
	t.Parallel()

	const (
		ourID   = 0x1234
		otherID = 0x4321
		seq     = 1
	)

	target4 := net.IPv4(198, 51, 100, 7).To4()
	target6 := net.ParseIP("2001:db8::7")
	elsewhere4 := net.IPv4(198, 51, 100, 8).To4()
	elsewhere6 := net.ParseIP("2001:db8::8")
	router := &net.IPAddr{IP: net.ParseIP(testRouter)}

	request4 := echoMessage(t, ipv4.ICMPTypeEcho, ourID, seq)
	request6 := echoMessage(t, ipv6.ICMPTypeEchoRequest, ourID, seq)

	tests := []struct {
		name    string
		isIPv6  bool
		useUDP  bool
		data    []byte
		want    error // nil: not answered at all
		success bool
	}{
		{
			name:    "positive control: our echo reply",
			data:    echoMessage(t, ipv4.ICMPTypeEchoReply, ourID, seq),
			success: true,
		},
		{
			name: "ttl exceeded quoting our request",
			data: timeExceeded(t, protocolICMP, 0, quotedDatagram(false, target4, protocolICMP, request4)),
			want: errTTLExceeded,
		},
		{
			name:   "ttl exceeded quoting our request, unprivileged socket",
			useUDP: true,
			data:   timeExceeded(t, protocolICMP, 0, quotedDatagram(false, target4, protocolICMP, request4)),
			want:   errTTLExceeded,
		},
		{
			name: "fragment reassembly time exceeded is not a ttl problem",
			data: timeExceeded(t, protocolICMP, timeExceededCodeReassembly,
				quotedDatagram(false, target4, protocolICMP, request4)),
			want: errReassemblyTimeExceeded,
		},
		{
			name: "quote of a request to another destination",
			data: timeExceeded(t, protocolICMP, 0, quotedDatagram(false, elsewhere4, protocolICMP, request4)),
		},
		{
			name: "quote of another process's request on a raw socket",
			data: timeExceeded(t, protocolICMP, 0,
				quotedDatagram(false, target4, protocolICMP, echoMessage(t, ipv4.ICMPTypeEcho, otherID, seq))),
		},
		{
			name: "quote of an echo reply, not a request",
			data: timeExceeded(t, protocolICMP, 0,
				quotedDatagram(false, target4, protocolICMP, echoMessage(t, ipv4.ICMPTypeEchoReply, ourID, seq))),
		},
		{
			name: "quote of a UDP datagram",
			data: timeExceeded(t, protocolICMP, 0, quotedDatagram(false, target4, 17, request4)),
		},
		{
			name: "quote truncated before the echo header",
			data: timeExceeded(t, protocolICMP, 0,
				quotedDatagram(false, target4, protocolICMP, request4)[:ipv4.HeaderLen+4]),
		},
		{
			name: "quote for a sequence never written",
			data: timeExceeded(t, protocolICMP, 0,
				quotedDatagram(false, target4, protocolICMP, echoMessage(t, ipv4.ICMPTypeEcho, ourID, 2))),
		},
		{
			name:   "ipv6 hop limit exceeded quoting our request",
			isIPv6: true,
			data:   timeExceeded(t, protocolICMPv6, 0, quotedDatagram(true, target6, protocolICMPv6, request6)),
			want:   errTTLExceeded,
		},
		{
			name:   "ipv6 quote of a request to another destination",
			isIPv6: true,
			data:   timeExceeded(t, protocolICMPv6, 0, quotedDatagram(true, elsewhere6, protocolICMPv6, request6)),
		},
		{
			name:   "ipv4 quote on an ipv6 socket",
			isIPv6: true,
			data:   timeExceeded(t, protocolICMPv6, 0, quotedDatagram(false, target4, protocolICMP, request4)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			matcher := &replyMatcher{
				target:       target4,
				proto:        protocolICMP,
				requestType:  ipv4.ICMPTypeEcho,
				replyType:    ipv4.ICMPTypeEchoReply,
				exceededType: ipv4.ICMPTypeTimeExceeded,
				useUDP:       tt.useUDP,
				id:           ourID,
				ttl:          3,
			}

			if tt.isIPv6 {
				matcher.target = target6
				matcher.proto = protocolICMPv6
				matcher.requestType = ipv6.ICMPTypeEchoRequest
				matcher.replyType = ipv6.ICMPTypeEchoReply
				matcher.exceededType = ipv6.ICMPTypeTimeExceeded
			}

			// Packets 0 and 1 are in flight; 2 was never written.
			state := newBurstState(3, time.Second)
			state.markSent(0)
			state.markSent(1)

			state.acceptReply(tt.data, router, matcher)

			out := state.collect()
			r.Len(out, 2)
			r.False(out[0].Success, "an untouched packet must not be answered")

			switch {
			case tt.success:
				r.True(out[1].Success)
			case tt.want != nil:
				r.False(out[1].Success, "a Time Exceeded must never count as a reply")
				r.ErrorIs(out[1].Error, tt.want)
				r.Contains(out[1].Error.Error(), testRouter)
			default:
				// Not ours: the packet is still waiting, so it reports the
				// plain per-packet timeout.
				r.False(out[1].Success)
				r.ErrorIs(out[1].Error, context.DeadlineExceeded)
			}
		})
	}
}

// TestPerformICMPPingsTimeExceededIsALostPacket: a TTL too low to reach the
// target gets every request discarded by a router. Those packets are lost,
// never received, and the Time Exceeded ends their wait right away instead
// of their timeout.
func TestPerformICMPPingsTimeExceededIsALostPacket(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	target := net.IPv4(127, 0, 0, 1).To4()

	_, restore := withFakeSocketMode(false, protocolICMP, func(c *fakeICMPConn, req []byte) {
		c.replies <- timeExceededFor(t, target, req)
	})
	defer restore()

	const timeout = 3 * time.Second

	start := time.Now()

	out, err := performICMPPings(context.Background(), target, false, burstParams{
		count: 3, timeout: timeout, interval: 10 * time.Millisecond, ttl: 1,
	})
	r.NoError(err)
	r.Len(out, 3)

	for _, res := range out {
		r.False(res.Success)
		r.ErrorIs(res.Error, errTTLExceeded)
		r.Contains(res.Error.Error(), "(ttl 1)")
	}

	r.Less(time.Since(start), timeout, "the burst waited out the timeout despite the router's answer")
}

// TestExecuteTimeExceeded checks the metrics and status a Time Exceeded
// produces, with a burst where only some packets die in transit as the
// negative case: those still count as lost, the others as received.
func TestExecuteTimeExceeded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		exceededSeqs map[int]bool
		wantStatus   checkerdef.Status
		wantReceived int
		wantLossPct  float64
	}{
		{"every packet discarded", map[int]bool{0: true, 1: true}, checkerdef.StatusDown, 0, 100},
		{"one packet discarded", map[int]bool{0: true}, checkerdef.StatusUp, 1, 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			burstHookMu.Lock()
			defer burstHookMu.Unlock()

			target := net.IPv4(127, 0, 0, 1).To4()

			_, restore := withFakeSocket(func(c *fakeICMPConn, req []byte) {
				if tt.exceededSeqs[requestSeq(req)] {
					c.replies <- timeExceededFor(t, target, req)

					return
				}

				if reply := echoReply(req); reply != nil {
					c.replies <- reply
				}
			})
			defer restore()

			result, err := (&ICMPChecker{}).Execute(context.Background(), &ICMPConfig{
				Host:     "127.0.0.1",
				Count:    2,
				Interval: 10 * time.Millisecond,
				Timeout:  time.Second,
				TTL:      1,
			})
			r.NoError(err)
			r.Equal(tt.wantStatus, result.Status)
			r.Equal(2, result.Metrics[metricPacketsSent])
			r.Equal(tt.wantReceived, result.Metrics[metricPacketsReceived])
			r.InDelta(tt.wantLossPct, result.Metrics[metricPacketLossPct], 0.001)

			if tt.wantStatus != checkerdef.StatusDown {
				return
			}

			errMsg, ok := result.Output[checkerdef.OutputKeyError].(string)
			r.True(ok)
			r.Contains(errMsg, "time to live exceeded")

			// A router answered: a decision on the path, not silence.
			r.NotNil(result.Diagnostics)
			r.NotNil(result.Diagnostics.NetworkFailure)
			r.Equal(checkerdef.NetFailureICMPUnreachable, result.Diagnostics.NetworkFailure.Class)
		})
	}
}
