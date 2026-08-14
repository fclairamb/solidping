package tlsedge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pires/go-proxyproto"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// Listener labels used in logs and in the
// solidping_tlsedge_connections_total{listener=...} counter.
const (
	listenerHTTP  = "http"
	listenerHTTPS = "https"
)

// Outcome labels for solidping_tlsedge_connections_total{outcome=...}. They
// partition every accepted connection: it is served here, handed to the next
// hop, refused by the loop guard, or lost because the next hop could not be
// reached.
const (
	outcomeLocal      = "local"
	outcomeForwarded  = "forwarded"
	outcomeRefused    = "refused"
	outcomeDialFailed = "dial_failed"
)

const (
	// fallbackDialTimeout bounds the connection attempt to the next hop. It has
	// to be well inside a CA's own validation timeout, because an ACME
	// challenge for a downstream domain travels through this dial.
	fallbackDialTimeout = 5 * time.Second
	// fallbackDecisionTimeout bounds the "is this host mine?" lookup, which may
	// reach the database (behind the status-page cache) on a cache miss.
	fallbackDecisionTimeout = 5 * time.Second
	// fallbackLinger bounds how long the surviving half of a spliced connection
	// may stay open after the other half is done, so a half-closed peer can
	// never pin a pair of file descriptors forever.
	fallbackLinger = 2 * time.Minute
	// fallbackAcceptBuffer smooths bursts between the peek goroutines and the
	// http.Server draining Accept.
	fallbackAcceptBuffer = 16
	// fallbackHopTLV carries, in the outbound PROXY v2 header, how many
	// SolidPing edges a connection has already traversed. It lives in the
	// PROXY protocol's vendor-custom TLV range.
	fallbackHopTLV = proxyproto.PP2_TYPE_MIN_CUSTOM
	// fallbackMaxHops bounds a chain. A correctly configured deployment uses
	// one or two hops; a cycle hits the ceiling within milliseconds and the
	// connection is refused instead of ping-ponging until fd exhaustion.
	fallbackMaxHops = 4
)

// errFallbackListenerClosed is what Accept reports once the listener is closed,
// mirroring net.ErrClosed so http.Server stops serving rather than spinning.
var errFallbackListenerClosed = net.ErrClosed

// fallbackConfig is everything the splitter needs to route one connection.
type fallbackConfig struct {
	// listener is "http" or "https": the log/metric label of the listener this
	// splitter sits on.
	listener string
	// upstream is the "host:port" next hop. Never empty — a splitter is only
	// installed when one is configured.
	upstream string
	// proxyProtocol prefixes each forwarded connection with a PROXY v2 header
	// carrying the original client address.
	proxyProtocol bool
	// peek reads the host out of a new connection, non-destructively.
	peek hostPeeker
	// peekTimeout bounds that read.
	peekTimeout time.Duration
	// isLocal reports whether this instance serves the host itself.
	isLocal func(ctx context.Context, host string) bool
	// log receives one line per forwarded, refused or dial-failed connection.
	log *slog.Logger
}

// fallbackStats are the in-process counters behind
// solidping_tlsedge_connections_total, kept per listener so a test can assert
// on one splitter without depending on a process-wide registry.
type fallbackStats struct {
	local      atomic.Int64
	forwarded  atomic.Int64
	refused    atomic.Int64
	dialFailed atomic.Int64
}

// fallbackListener splits accepted connections between the local TLS/HTTP stack
// and a configured next hop, deciding on the hostname the connection asks for
// BEFORE anything is terminated.
//
// Accept only ever returns connections this instance serves, so everything
// above it (certmagic, crypto/tls, net/http) is untouched. Connections for
// hosts this instance does not serve never reach Accept: they are spliced to
// the upstream on their own goroutine.
//
// Peeking is per-connection work on a per-connection goroutine — a client that
// opens a connection and says nothing stalls only itself, never the accept
// loop.
type fallbackListener struct {
	net.Listener

	cfg   fallbackConfig
	stats fallbackStats

	conns chan net.Conn

	closeOnce sync.Once
	done      chan struct{}

	mu        sync.Mutex
	acceptErr error

	// loopLogged remembers the peers already reported by the loop guard, so a
	// misconfigured cycle produces one line per offending peer rather than one
	// per connection.
	loopLogged sync.Map
}

// ErrNoFallbackUpstream is returned when a splitter is built without a next
// hop. config.validateACMEFallbackUpstreams already rejects that at startup;
// this is the second lock, so a listener can never end up silently dropping
// every unknown-host connection.
var ErrNoFallbackUpstream = errors.New("tlsedge: a fallback upstream address is required")

// newFallbackListener wraps inner and starts its accept loop.
func newFallbackListener(ctx context.Context, inner net.Listener, cfg fallbackConfig) (*fallbackListener, error) {
	if strings.TrimSpace(cfg.upstream) == "" {
		return nil, ErrNoFallbackUpstream
	}

	if cfg.peek == nil || cfg.isLocal == nil {
		return nil, ErrNoFallbackUpstream
	}

	if cfg.log == nil {
		cfg.log = slog.Default()
	}

	if cfg.peekTimeout <= 0 {
		cfg.peekTimeout = readHeaderTimeout
	}

	listener := &fallbackListener{
		Listener: inner,
		cfg:      cfg,
		conns:    make(chan net.Conn, fallbackAcceptBuffer),
		done:     make(chan struct{}),
	}

	// The accept loop outlives the caller's context (it is stopped by Close), so
	// the per-connection goroutines inherit its values without its
	// cancellation — a canceled context would abort connections mid-splice.
	go listener.acceptLoop(context.WithoutCancel(ctx))

	return listener, nil
}

// Accept returns the next connection this instance serves itself.
func (l *fallbackListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		// Drain what the peek goroutines already handed over before reporting
		// the listener closed.
		select {
		case conn := <-l.conns:
			return conn, nil
		default:
			return nil, l.err()
		}
	}
}

// Close stops the accept loop and the underlying listener. Connections already
// spliced to the upstream are left to finish on their own goroutines; they are
// no longer this listener's business.
func (l *fallbackListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })

	return l.Listener.Close() //nolint:wrapcheck // transparent pass-through
}

// acceptLoop accepts raw connections and hands each to its own goroutine, so a
// slow or silent peer never delays the next accept.
func (l *fallbackListener) acceptLoop(ctx context.Context) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			l.stop(err)

			return
		}

		go l.handle(ctx, conn)
	}
}

// stop records the terminal accept error and unblocks Accept.
func (l *fallbackListener) stop(err error) {
	l.mu.Lock()
	if l.acceptErr == nil {
		l.acceptErr = err
	}
	l.mu.Unlock()

	l.closeOnce.Do(func() { close(l.done) })
}

func (l *fallbackListener) err() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.acceptErr != nil {
		return l.acceptErr
	}

	return errFallbackListenerClosed
}

// handle decides where one connection goes. Anything it cannot read or parse
// stays local: a connection whose host is unknown is NEVER forwarded, because
// forwarding it would hand a possibly-ours connection to a hop that certainly
// cannot serve it.
func (l *fallbackListener) handle(ctx context.Context, conn net.Conn) {
	host, buffered, err := l.cfg.peek(conn, l.cfg.peekTimeout)
	replayed := newPrefixConn(conn, buffered)

	if err != nil || host == "" {
		l.cfg.log.DebugContext(ctx, "tlsedge: keeping a connection local, no routable host",
			"listener", l.cfg.listener, "peer", peerAddr(conn), "error", err)
		l.deliverLocal(replayed)

		return
	}

	decisionCtx, cancel := context.WithTimeout(ctx, fallbackDecisionTimeout)
	defer cancel()

	if l.cfg.isLocal(decisionCtx, host) {
		l.deliverLocal(replayed)

		return
	}

	l.forward(decisionCtx, replayed, host)
}

// deliverLocal queues a connection for Accept, closing it if the listener shut
// down in the meantime.
func (l *fallbackListener) deliverLocal(conn net.Conn) {
	select {
	case l.conns <- conn:
		l.record(outcomeLocal)
	case <-l.done:
		_ = conn.Close()
	}
}

// forward hands a connection to the next hop: loop guard, dial, PROXY v2
// header, replay of every peeked byte, then a raw splice. Every failure closes
// the client connection — a host we decided is not ours must never fall back to
// local termination, which would refuse it with our own certificate error and
// hide the real fault.
func (l *fallbackListener) forward(ctx context.Context, conn net.Conn, host string) {
	hops := inboundHopCount(conn)
	if reason, looped := l.loopReason(conn, hops); looped {
		l.refuse(ctx, conn, host, reason)

		return
	}

	dialer := net.Dialer{Timeout: fallbackDialTimeout}

	upstream, err := dialer.DialContext(ctx, "tcp", l.cfg.upstream)
	if err != nil {
		l.record(outcomeDialFailed)
		l.cfg.log.ErrorContext(ctx, "tlsedge: cannot reach the fallback upstream, dropping the connection",
			"listener", l.cfg.listener, "host", host, "upstream", l.cfg.upstream,
			"client", conn.RemoteAddr().String(), "error", err)
		_ = conn.Close()

		return
	}

	if l.cfg.proxyProtocol {
		if headerErr := writeProxyHeader(upstream, conn, hops+1); headerErr != nil {
			l.record(outcomeDialFailed)
			l.cfg.log.ErrorContext(ctx, "tlsedge: cannot send the PROXY header to the fallback upstream",
				"listener", l.cfg.listener, "host", host, "upstream", l.cfg.upstream, "error", headerErr)
			_ = upstream.Close()
			_ = conn.Close()

			return
		}
	}

	l.record(outcomeForwarded)
	l.cfg.log.InfoContext(ctx, "tlsedge: forwarding to the fallback upstream",
		"listener", l.cfg.listener, "host", host, "upstream", l.cfg.upstream,
		"client", conn.RemoteAddr().String(), "hop", hops+1)

	splice(conn, upstream)
}

// refuse drops a connection the loop guard rejected, logging once per offending
// peer so a cycle does not drown the log.
func (l *fallbackListener) refuse(ctx context.Context, conn net.Conn, host, reason string) {
	l.record(outcomeRefused)

	peer := peerAddr(conn)
	// Keyed on the address without its port: a cycle opens a new ephemeral
	// source port per connection, so keying on the full address would log every
	// one of them.
	if _, seen := l.loopLogged.LoadOrStore(hostOfAddr(peer), struct{}{}); !seen {
		l.cfg.log.ErrorContext(ctx, "tlsedge: refusing to forward, the fallback chain loops back here",
			"listener", l.cfg.listener, "host", host, "peer", peer,
			"upstream", l.cfg.upstream, "reason", reason)
	}

	_ = conn.Close()
}

// loopReason implements the loop guard. Two independent rules, because neither
// covers every topology:
//
//   - the immediate peer is the configured upstream — the direct A→B→A cycle,
//     caught on the second hop before anything is dialed. Compared on the IP
//     only: the return connection's source port is ephemeral, so an endpoint
//     comparison would never match. Skipped when the upstream is a loopback
//     address, where every instance shares 127.0.0.1 and the peer address
//     genuinely cannot distinguish a cycle from a local client;
//   - the hop count carried in the inbound PROXY v2 header reached
//     fallbackMaxHops — a TTL that bounds any cycle, including the loopback
//     one, as long as the previous hop is trusted for PROXY protocol.
func (l *fallbackListener) loopReason(conn net.Conn, hops int) (string, bool) {
	if hops >= fallbackMaxHops {
		return "hop limit reached", true
	}

	if peerIsUpstream(peerAddr(conn), l.cfg.upstream) {
		return "the immediate peer is the configured upstream", true
	}

	return "", false
}

// record bumps both the in-process counter and the Prometheus one.
func (l *fallbackListener) record(outcome string) {
	switch outcome {
	case outcomeLocal:
		l.stats.local.Add(1)
	case outcomeForwarded:
		l.stats.forwarded.Add(1)
	case outcomeRefused:
		l.stats.refused.Add(1)
	case outcomeDialFailed:
		l.stats.dialFailed.Add(1)
	}

	prommetrics.TLSEdgeConnections.WithLabelValues(l.cfg.listener, outcome).Inc()
}

// peerIsUpstream reports whether the immediate peer of an accepted connection
// is the host this splitter forwards to. See loopReason for why this is an
// IP-level comparison and why loopback is excluded.
func peerIsUpstream(peer, upstream string) bool {
	upstreamHost, _, err := net.SplitHostPort(strings.TrimSpace(upstream))
	if err != nil {
		return false
	}

	peerHost, _, err := net.SplitHostPort(peer)
	if err != nil {
		peerHost = peer
	}

	upstreamIP := net.ParseIP(strings.Trim(upstreamHost, "[]"))
	if upstreamIP == nil {
		// A named upstream is not resolved here: a DNS lookup per connection
		// would be a new failure mode on the hot path, and the hop-count TTL
		// covers the cycle anyway.
		return strings.EqualFold(peerHost, upstreamHost)
	}

	if upstreamIP.IsLoopback() {
		return false
	}

	peerIP := net.ParseIP(strings.Trim(peerHost, "[]"))

	return peerIP != nil && peerIP.Equal(upstreamIP)
}

// inboundHopCount reads how many SolidPing edges this connection has already
// been forwarded through, from the TLV of the PROXY header the previous hop
// sent. Zero when there is no header, no TLV, or the peer was not trusted
// enough for its header to be honored.
func inboundHopCount(conn net.Conn) int {
	proxied, ok := proxyConn(conn)
	if !ok {
		return 0
	}

	header := proxied.ProxyHeader()
	if header == nil {
		return 0
	}

	tlvs, err := header.TLVs()
	if err != nil {
		return 0
	}

	for index := range tlvs {
		if tlv := &tlvs[index]; tlv.Type == fallbackHopTLV && len(tlv.Value) == 1 {
			return int(tlv.Value[0])
		}
	}

	return 0
}

// writeProxyHeader prefixes the upstream connection with a PROXY v2 header
// carrying the ORIGINAL client address — conn.RemoteAddr(), which the inbound
// wrapper already rewrote to the real client when this instance is itself
// behind a proxy — plus the hop count that bounds a cycle.
func writeProxyHeader(upstream net.Conn, conn net.Conn, hop int) error {
	header := proxyproto.HeaderProxyFromAddrs(2, conn.RemoteAddr(), conn.LocalAddr())

	if hop > 0 && hop <= fallbackMaxHops {
		if err := header.SetTLVs([]proxyproto.TLV{{
			Type:  fallbackHopTLV,
			Value: []byte{byte(hop)},
		}}); err != nil {
			return err //nolint:wrapcheck // the caller logs it with full context
		}
	}

	if _, err := header.WriteTo(upstream); err != nil {
		return err //nolint:wrapcheck // the caller logs it with full context
	}

	return nil
}

// splice copies a forwarded connection in both directions until both are done,
// half-closing each side as its source ends so a response is never truncated,
// and bounding the surviving half so a half-closed peer cannot pin the pair
// forever.
func splice(client, upstream net.Conn) {
	defer func() {
		_ = client.Close()
		_ = upstream.Close()
	}()

	var waitGroup sync.WaitGroup

	waitGroup.Add(2)

	copyHalf := func(dst, src net.Conn) {
		defer waitGroup.Done()

		_, _ = io.Copy(dst, src)

		// The source is done: tell the destination so it can finish its own
		// reply, and put a ceiling on how long the other direction may linger.
		closeWrite(dst)
		_ = dst.SetDeadline(time.Now().Add(fallbackLinger))
		_ = src.SetDeadline(time.Now().Add(fallbackLinger))
	}

	go copyHalf(upstream, client)
	go copyHalf(client, upstream)

	waitGroup.Wait()
}

// closeWriter is implemented by *net.TCPConn: a half-close tells the peer this
// direction is finished without tearing down the reply direction.
type closeWriter interface {
	CloseWrite() error
}

// closeWrite half-closes conn when the underlying socket supports it.
func closeWrite(conn net.Conn) {
	if half, ok := rawConn(conn).(closeWriter); ok {
		_ = half.CloseWrite()
	}
}

// hostOfAddr strips the port from a "host:port" address, leaving anything
// unparseable untouched.
func hostOfAddr(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}

	return addr
}

// peerAddr is the address of the IMMEDIATE peer — before any PROXY header
// rewrite, which reports the original client instead. The loop guard needs the
// former: the machine that opened this TCP connection.
func peerAddr(conn net.Conn) string {
	addr := rawConn(conn).RemoteAddr()
	if addr == nil {
		return ""
	}

	return addr.String()
}

// proxyConn digs the PROXY protocol wrapper out of a connection, past the
// replay wrapper the splitter adds.
func proxyConn(conn net.Conn) (*proxyproto.Conn, bool) {
	proxied, ok := unwrapPrefix(conn).(*proxyproto.Conn)

	return proxied, ok
}

// rawConn returns the underlying transport connection, past the replay and
// PROXY protocol wrappers.
func rawConn(conn net.Conn) net.Conn {
	unwrapped := unwrapPrefix(conn)
	if proxied, ok := unwrapped.(*proxyproto.Conn); ok {
		return proxied.Raw()
	}

	return unwrapped
}

func unwrapPrefix(conn net.Conn) net.Conn {
	if prefixed, ok := conn.(*prefixConn); ok {
		return prefixed.Conn
	}

	return conn
}
