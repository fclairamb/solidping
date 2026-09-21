package checkicmp

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// errFakeListenDenied stands in for a socket setup failure.
var errFakeListenDenied = errors.New("permission denied")

// burstHookMu serializes tests that swap openBurstSocket (a package-level
// hook) — parallel tests must not race each other's hook.
var burstHookMu sync.Mutex //nolint:gochecknoglobals // serializes the openBurstSocket hook across parallel tests

// writeRecord is one packet the fake conn accepted, with its write time so
// tests can check the send schedule.
type writeRecord struct {
	data []byte
	at   time.Time
}

// fakeICMPConn is an in-memory net.PacketConn standing in for the ICMP
// socket. Writes are recorded and handed to an onWrite hook that decides
// whether to echo a reply back (through the replies channel) or drop the
// packet (simulated loss). Reads honor SetReadDeadline.
type fakeICMPConn struct {
	mu           sync.Mutex
	writes       []writeRecord
	readDeadline time.Time
	closed       chan struct{}
	closeOnce    sync.Once
	replies      chan []byte
	onWrite      func(req []byte)
}

func newFakeICMPConn(onWrite func(req []byte)) *fakeICMPConn {
	return &fakeICMPConn{
		closed:  make(chan struct{}),
		replies: make(chan []byte, 1024),
		onWrite: onWrite,
	}
}

func (c *fakeICMPConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	record := writeRecord{data: append([]byte(nil), b...), at: time.Now()}

	c.mu.Lock()
	c.writes = append(c.writes, record)
	hook := c.onWrite
	c.mu.Unlock()

	if hook != nil {
		hook(record.data)
	}

	return len(b), nil
}

func (c *fakeICMPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	deadline := c.readDeadline
	c.mu.Unlock()

	var timer <-chan time.Time

	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil, os.ErrDeadlineExceeded
		}

		timer = time.After(remaining)
	}

	select {
	case b := <-c.replies:
		n := copy(p, b)

		return n, &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
	case <-timer:
		return 0, nil, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *fakeICMPConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()

	return nil
}

func (c *fakeICMPConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *fakeICMPConn) SetWriteDeadline(_ time.Time) error { return nil }

func (c *fakeICMPConn) LocalAddr() net.Addr { return &net.IPAddr{IP: net.IPv4zero} }

func (c *fakeICMPConn) RemoteAddr() net.Addr { return nil }

func (c *fakeICMPConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })

	return nil
}

func (c *fakeICMPConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.writes)
}

func (c *fakeICMPConn) writeTimes() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	times := make([]time.Time, 0, len(c.writes))
	for _, record := range c.writes {
		times = append(times, record.at)
	}

	return times
}

// echoReply builds an Echo Reply datagram answering an Echo Request.
func echoReply(req []byte) []byte {
	msg, err := icmp.ParseMessage(protocolICMP, req)
	if err != nil {
		return nil
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		return nil
	}

	reply := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{ID: echo.ID, Seq: echo.Seq, Data: echo.Data},
	}

	out, err := reply.Marshal(nil)
	if err != nil {
		return nil
	}

	return out
}

// withFakeSocket swaps openBurstSocket for a fake responder. Returns the conn
// and a restore func (call before the test ends).
func withFakeSocket(onWrite func(conn *fakeICMPConn, req []byte)) (*fakeICMPConn, func()) {
	var conn *fakeICMPConn

	conn = newFakeICMPConn(func(req []byte) {
		onWrite(conn, req)
	})

	previous := openBurstSocket

	openBurstSocket = func(bool) (net.PacketConn, bool, int, error) {
		return conn, true, protocolICMP, nil
	}

	return conn, func() { openBurstSocket = previous }
}

func withEchoAll() (*fakeICMPConn, func()) {
	return withFakeSocket(func(c *fakeICMPConn, req []byte) {
		if reply := echoReply(req); reply != nil {
			c.replies <- reply
		}
	})
}

func TestPerformICMPPingsAllPacketsAnsweredOnSchedule(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	conn, restore := withFakeSocket(func(c *fakeICMPConn, req []byte) {
		if reply := echoReply(req); reply != nil {
			c.replies <- reply
		}
	})
	defer restore()

	const (
		count    = 5
		interval = 20 * time.Millisecond
		timeout  = 500 * time.Millisecond
	)

	start := time.Now()

	out, err := performICMPPings(context.Background(), net.IPv4(127, 0, 0, 1), false, count, timeout, interval)
	r.NoError(err)

	// All `count` packets were attempted and answered.
	r.Len(out, count)

	for _, res := range out {
		r.True(res.Success)
		r.Greater(res.RTT, time.Duration(0))
	}

	// The send schedule was honored: first write immediately, the rest one
	// `interval` apart (generous tolerance for CI scheduling jitter).
	times := conn.writeTimes()
	r.Len(times, count)

	spread := times[count-1].Sub(times[0])
	want := time.Duration(count-1) * interval
	r.Less(spread, want+150*time.Millisecond)
	r.Greater(spread, want-20*time.Millisecond)

	// Concurrency: the burst did NOT take count × timeout (the sequential
	// worst case) — run time is (count-1) × interval + reply collection.
	r.Less(time.Since(start), timeout*time.Duration(count))
}

func TestPerformICMPPingsLostPacketsReportedAsLoss(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	_, restore := withFakeSocket(func(c *fakeICMPConn, req []byte) {
		msg, err := icmp.ParseMessage(protocolICMP, req)
		if err != nil {
			return
		}

		echo, ok := msg.Body.(*icmp.Echo)
		if !ok {
			return
		}

		// Drop seq 1 and 3: a burst that meets real packet loss.
		if echo.Seq == 1 || echo.Seq == 3 {
			return
		}

		if reply := echoReply(req); reply != nil {
			c.replies <- reply
		}
	})
	defer restore()

	const (
		count    = 5
		interval = 20 * time.Millisecond
		timeout  = 300 * time.Millisecond
	)

	start := time.Now()

	out, err := performICMPPings(context.Background(), net.IPv4(127, 0, 0, 1), false, count, timeout, interval)
	r.NoError(err)

	// Every packet was still attempted — loss does not shorten the burst.
	r.Len(out, count)

	received := 0

	for _, res := range out {
		if res.Success {
			received++

			continue
		}

		r.ErrorIs(res.Error, context.DeadlineExceeded)
	}

	r.Equal(3, received)

	// Run time independent of loss: bounded by (count-1) × interval + timeout,
	// NOT count × timeout (the sequential worst case this rework removes).
	r.Less(time.Since(start), time.Duration(count)*timeout)
}

func TestPerformICMPPingsTruncatedBurstShrinksSent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	conn, restore := withEchoAll()
	defer restore()

	const (
		count    = 10
		interval = 50 * time.Millisecond
		timeout  = 1 * time.Second
	)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	out, err := performICMPPings(ctx, net.IPv4(127, 0, 0, 1), false, count, timeout, interval)
	r.NoError(err)

	// The budget cut the schedule short: only what was actually written is
	// reported — packets_sent shrinks, it never invents lost packets.
	writes := conn.writeCount()
	r.Len(out, writes)
	r.Positive(writes)
	r.Less(writes, count)

	for _, res := range out {
		r.True(res.Success, "unanswered packet: %+v err=%v", res, res.Error)
	}
}

func TestPerformICMPPingsSocketSetupFailureIsFatal(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	previous := openBurstSocket
	openBurstSocket = func(bool) (net.PacketConn, bool, int, error) {
		return nil, false, 0, errFakeListenDenied
	}
	defer func() { openBurstSocket = previous }()

	out, err := performICMPPings(context.Background(), net.IPv4(127, 0, 0, 1), false, 3, time.Second, 50*time.Millisecond)
	r.Error(err)
	r.Nil(out)
}

func TestExecuteReportsJitterAndTruthfulCounts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	_, restore := withFakeSocket(func(c *fakeICMPConn, req []byte) {
		if reply := echoReply(req); reply != nil {
			c.replies <- reply
		}
	})
	defer restore()

	checker := &ICMPChecker{}

	result, err := checker.Execute(context.Background(), &ICMPConfig{
		Host:     "127.0.0.1",
		Count:    5,
		Interval: 20 * time.Millisecond,
		Timeout:  500 * time.Millisecond,
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)

	r.Equal(5, result.Metrics[metricPacketsSent])
	r.Equal(5, result.Metrics[metricPacketsReceived])
	r.InDelta(float64(0), result.Metrics[metricPacketLossPct], 0.001)

	// Jitter sits alongside the min/max/avg family.
	jitter, ok := result.Metrics[metricRTTJitterMs].(float64)
	r.True(ok, "rtt_ms_jitter must be a float64")
	r.GreaterOrEqual(jitter, float64(0))

	for _, key := range []string{"rtt_ms_min", "rtt_ms_max", "rtt_ms_avg"} {
		_, ok := result.Metrics[key].(float64)
		r.True(ok, "%s must be present", key)
	}
}

func TestExecuteShrinksPacketsSentWhenTruncated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	burstHookMu.Lock()
	defer burstHookMu.Unlock()

	conn, restore := withEchoAll()
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	checker := &ICMPChecker{}

	result, err := checker.Execute(ctx, &ICMPConfig{
		Host:     "127.0.0.1",
		Count:    100,
		Interval: 50 * time.Millisecond,
		Timeout:  time.Second,
	})
	r.NoError(err)

	writes := conn.writeCount()
	r.Positive(writes)
	r.Less(writes, 100)

	// packets_sent equals what was really transmitted, and the loss figure is
	// computed over that — not over the 100 packets the schedule planned.
	r.Equal(writes, result.Metrics[metricPacketsSent])
	r.Equal(writes, result.Metrics[metricPacketsReceived])
	r.InDelta(float64(0), result.Metrics[metricPacketLossPct], 0.001)
}

func TestBurstBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tests := []struct {
		name string
		cfg  ICMPConfig
		want time.Duration
	}{
		{
			name: "defaults: count 1 × timeout 5s",
			cfg:  ICMPConfig{},
			want: 5 * time.Second,
		},
		{
			name: "count 10, timeout 1s, interval 100ms",
			cfg:  ICMPConfig{Count: 10, Timeout: time.Second, Interval: 100 * time.Millisecond},
			want: 10*time.Second + 9*100*time.Millisecond,
		},
		{
			name: "count 600, timeout 30s, interval 50ms",
			cfg:  ICMPConfig{Count: 600, Timeout: 30 * time.Second, Interval: 50 * time.Millisecond},
			want: 600*30*time.Second + 599*50*time.Millisecond,
		},
		{
			name: "interval default (1s) applies when unset",
			cfg:  ICMPConfig{Count: 3, Timeout: 2 * time.Second},
			want: 6*time.Second + 2*time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := tt.cfg
			r.Equal(tt.want, cfg.BurstBudget())
		})
	}
}
