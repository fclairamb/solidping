package discovery

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// packetConn is the slice of net.PacketConn the pinger uses, so tests can
// inject a fake connection.
type packetConn interface {
	ReadFrom(buf []byte) (int, net.Addr, error)
	WriteTo(data []byte, dst net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

// listenFunc opens an ICMP packet connection (the icmp.ListenPacket shape).
type listenFunc func(network, address string) (packetConn, error)

func listenICMPPacket(network, address string) (packetConn, error) {
	return icmp.ListenPacket(network, address)
}

// replyKey identifies a pending probe: the host it was sent to and its sequence number.
type replyKey struct {
	ip  string
	seq uint16
}

// icmpPinger shares one ICMP socket between concurrent probes. A single reader
// goroutine demultiplexes echo replies to the waiting probe by (source IP, seq),
// so probes never consume each other's replies.
type icmpPinger struct {
	conn    packetConn
	udp     bool // unprivileged datagram socket: the kernel rewrites the echo ID
	id      int
	seq     atomic.Uint32
	mu      sync.Mutex
	pending map[replyKey]chan struct{}
	done    chan struct{}
}

// newICMPPinger opens the socket, unprivileged udp4 first and raw ip4:icmp
// second (same order as checkers/checkicmp listenICMP, which is unexported).
// It returns nil when neither is available.
func newICMPPinger(listen listenFunc) *icmpPinger {
	useUDP := true

	conn, err := listen("udp4", "0.0.0.0")
	if err != nil {
		useUDP = false

		conn, err = listen("ip4:icmp", "0.0.0.0")
		if err != nil {
			return nil
		}
	}

	pinger := &icmpPinger{
		conn:    conn,
		udp:     useUDP,
		id:      os.Getpid() & 0xffff,
		pending: make(map[replyKey]chan struct{}),
		done:    make(chan struct{}),
	}

	go pinger.readLoop()

	return pinger
}

// close stops the reader and releases the socket.
func (p *icmpPinger) close() {
	_ = p.conn.Close()
	<-p.done
}

func (p *icmpPinger) readLoop() {
	defer close(p.done)

	buf := make([]byte, 1500)

	for {
		n, from, err := p.conn.ReadFrom(buf)
		if err != nil {
			return // the socket was closed
		}

		p.deliver(buf[:n], from)
	}
}

// deliver hands an echo reply to the probe waiting for it; anything else is dropped.
func (p *icmpPinger) deliver(data []byte, from net.Addr) {
	msg, err := icmp.ParseMessage(icmpProtocol, data)
	if err != nil || msg.Type != ipv4.ICMPTypeEchoReply {
		return
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok || (!p.udp && echo.ID != p.id) {
		return
	}

	var srcIP net.IP

	switch addr := from.(type) {
	case *net.IPAddr:
		srcIP = addr.IP
	case *net.UDPAddr:
		srcIP = addr.IP
	default:
		return
	}

	key := replyKey{ip: srcIP.String(), seq: uint16(echo.Seq)}

	p.mu.Lock()
	replyCh, found := p.pending[key]
	delete(p.pending, key)
	p.mu.Unlock()

	if found {
		close(replyCh)
	}
}

// ping sends one echo request to ipStr and reports whether its own reply came back in time.
func (p *icmpPinger) ping(ipStr string, timeout time.Duration) bool {
	ip := net.ParseIP(ipStr).To4()
	if ip == nil {
		return false
	}

	seq := uint16(p.seq.Add(1))
	key := replyKey{ip: ip.String(), seq: seq}
	replyCh := make(chan struct{})

	p.mu.Lock()
	p.pending[key] = replyCh
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
	}()

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: p.id, Seq: int(seq), Data: []byte("solidping")},
	}

	data, err := msg.Marshal(nil)
	if err != nil {
		return false
	}

	var dst net.Addr = &net.IPAddr{IP: ip}
	if p.udp {
		dst = &net.UDPAddr{IP: ip}
	}

	if err = p.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}

	if _, err = p.conn.WriteTo(data, dst); err != nil {
		return false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-replyCh:
		return true
	case <-timer.C:
		return false
	}
}
