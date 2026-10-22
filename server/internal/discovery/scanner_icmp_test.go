package discovery

import (
	"errors"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

var errNotPermitted = errors.New("operation not permitted")

type fakeDatagram struct {
	data []byte
	from net.Addr
}

// fakeConn is an in-memory packetConn. WriteTo records the request; replies are
// injected by the test through inject.
type fakeConn struct {
	incoming chan fakeDatagram
	closed   chan struct{}
	once     sync.Once

	mu     sync.Mutex
	writes []fakeWrite
	onSend func(dst net.Addr, seq int)
}

type fakeWrite struct {
	dst net.Addr
	id  int
	seq int
}

func newFakeConn() *fakeConn {
	return &fakeConn{incoming: make(chan fakeDatagram, 256), closed: make(chan struct{})}
}

func (f *fakeConn) ReadFrom(buf []byte) (int, net.Addr, error) {
	select {
	case dg := <-f.incoming:
		return copy(buf, dg.data), dg.from, nil
	case <-f.closed:
		return 0, nil, net.ErrClosed
	}
}

func (f *fakeConn) WriteTo(data []byte, dst net.Addr) (int, error) {
	msg, err := icmp.ParseMessage(icmpProtocol, data)
	if err != nil {
		return 0, err
	}

	echo, _ := msg.Body.(*icmp.Echo)

	f.mu.Lock()
	f.writes = append(f.writes, fakeWrite{dst: dst, id: echo.ID, seq: echo.Seq})
	hook := f.onSend
	f.mu.Unlock()

	if hook != nil {
		hook(dst, echo.Seq)
	}

	return len(data), nil
}

func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func (f *fakeConn) Close() error {
	f.once.Do(func() { close(f.closed) })

	return nil
}

func (f *fakeConn) inject(t *testing.T, from string, id, seq int, udp bool) {
	t.Helper()

	data, err := (&icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Body: &icmp.Echo{ID: id, Seq: seq},
	}).Marshal(nil)
	require.NoError(t, err)

	var addr net.Addr = &net.IPAddr{IP: net.ParseIP(from)}
	if udp {
		addr = &net.UDPAddr{IP: net.ParseIP(from)}
	}

	f.incoming <- fakeDatagram{data: data, from: addr}
}

func (f *fakeConn) lastWrite(t *testing.T) fakeWrite {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.writes)

	return f.writes[len(f.writes)-1]
}

func fakeListen(conn *fakeConn, failNetworks ...string) listenFunc {
	return func(network, _ string) (packetConn, error) {
		for _, n := range failNetworks {
			if n == network {
				return nil, errNotPermitted
			}
		}

		return conn, nil
	}
}

// pingAsync pings in a goroutine and waits until the probe has been written.
func pingAsync(t *testing.T, p *icmpPinger, conn *fakeConn, ip string, timeout time.Duration) (chan bool, fakeWrite) {
	t.Helper()

	before := func() int { conn.mu.Lock(); defer conn.mu.Unlock(); return len(conn.writes) }()
	res := make(chan bool, 1)

	go func() { res <- p.ping(ip, timeout) }()

	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()

		return len(conn.writes) > before
	}, 2*time.Second, time.Millisecond)

	return res, conn.lastWrite(t)
}

func TestICMPPingerMatchesOwnReply(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	p := newICMPPinger(fakeListen(conn, "udp4"))
	require.NotNil(t, p)
	defer p.close()

	res, w := pingAsync(t, p, conn, "10.0.0.1", 2*time.Second)
	conn.inject(t, "10.0.0.1", p.id, w.seq, false)

	require.True(t, <-res, "positive control: own reply marks the host reachable")
}

func TestICMPPingerIgnoresForeignReplies(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	p := newICMPPinger(fakeListen(conn, "udp4"))
	require.NotNil(t, p)
	defer p.close()

	res, w := pingAsync(t, p, conn, "10.0.0.1", 300*time.Millisecond)
	conn.inject(t, "10.0.0.2", p.id, w.seq, false)   // other host, right seq
	conn.inject(t, "10.0.0.1", p.id, w.seq+1, false) // right host, wrong seq
	conn.inject(t, "10.0.0.1", p.id+1, w.seq, false) // raw socket: wrong ID
	conn.inject(t, "10.0.0.9", p.id, 4242, false)    // no pending probe

	require.False(t, <-res)
}

func TestICMPPingerUDPIgnoresID(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	p := newICMPPinger(fakeListen(conn))
	require.NotNil(t, p)
	require.True(t, p.udp)
	defer p.close()

	res, w := pingAsync(t, p, conn, "10.0.0.1", 2*time.Second)
	_, isUDP := w.dst.(*net.UDPAddr)
	require.True(t, isUDP, "udp4 destination is a *net.UDPAddr")
	conn.inject(t, "10.0.0.1", 777, w.seq, true) // the kernel rewrote the ID

	require.True(t, <-res)
}

func TestICMPPingerConcurrentNoCrosstalk(t *testing.T) {
	t.Parallel()

	const hosts = 64

	conn := newFakeConn()
	p := newICMPPinger(fakeListen(conn, "udp4"))
	require.NotNil(t, p)
	defer p.close()

	// Only even-numbered hosts answer; replies are injected in random order.
	var (
		mu      sync.Mutex
		replies []func()
	)

	conn.onSend = func(dst net.Addr, seq int) {
		ip := dst.(*net.IPAddr).IP.String()                     //nolint:forcetypeassert // test
		if last := dst.(*net.IPAddr).IP.To4()[3]; last%2 != 0 { //nolint:forcetypeassert // test
			return
		}

		mu.Lock()
		replies = append(replies, func() { conn.inject(t, ip, p.id, seq, false) })
		mu.Unlock()
	}

	results := make([]bool, hosts)

	var wg sync.WaitGroup
	for i := range hosts {
		wg.Add(1)

		go func() {
			defer wg.Done()

			results[i] = p.ping(net.IPv4(10, 0, 1, byte(i)).String(), time.Second)
		}()
	}

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(replies) == hosts/2
	}, 2*time.Second, time.Millisecond)

	mu.Lock()
	rand.Shuffle(len(replies), func(a, b int) { replies[a], replies[b] = replies[b], replies[a] })

	for _, send := range replies {
		send()
	}
	mu.Unlock()

	wg.Wait()

	for i, got := range results {
		require.Equal(t, i%2 == 0, got, "host %d", i)
	}
}

func TestICMPPingerSocketSelection(t *testing.T) {
	t.Parallel()

	t.Run("udp4 preferred", func(t *testing.T) {
		t.Parallel()

		p := newICMPPinger(fakeListen(newFakeConn()))
		require.NotNil(t, p)
		defer p.close()
		require.True(t, p.udp)
	})

	t.Run("raw when udp4 fails", func(t *testing.T) {
		t.Parallel()

		p := newICMPPinger(fakeListen(newFakeConn(), "udp4"))
		require.NotNil(t, p)
		defer p.close()
		require.False(t, p.udp)
	})

	t.Run("both fail is TCP-only", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, newICMPPinger(fakeListen(newFakeConn(), "udp4", "ip4:icmp")))

		res := runProbes(t.Context(), nil, scanParams{concurrency: 1, timeout: time.Second},
			fakeListen(newFakeConn(), "udp4", "ip4:icmp"))
		require.False(t, res.ICMPAvailable)

		res = runProbes(t.Context(), nil, scanParams{concurrency: 1, timeout: time.Second}, fakeListen(newFakeConn()))
		require.True(t, res.ICMPAvailable)
	})
}
