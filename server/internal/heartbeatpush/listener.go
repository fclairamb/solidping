package heartbeatpush

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// Transport labels, used both for the Prometheus label and for the value
// stored on the result under output.data.transport.
const (
	TransportTCP = "tcp"
	TransportUDP = "udp"
)

// Beat outcomes, as reported on prommetrics.HeartbeatPushBeats.
const (
	outcomeAccepted    = "accepted"
	outcomeMalformed   = "malformed"
	outcomeRejected    = "rejected"
	outcomeRateLimited = "rate_limited"
	outcomeError       = "error"
)

// okReply is what an accepted beat is answered with. Two bytes: on UDP it can
// never exceed the datagram that triggered it (a valid beat is dozens of bytes
// at minimum), so the listener is not an amplification vector.
const okReply = "OK"

// errLineTooLong closes a TCP connection whose sender exceeded MaxLineBytes.
// It never reaches the wire — the connection is simply closed.
var errLineTooLong = errors.New("line too long")

// maxUDPDatagram bounds a single read. One datagram carries exactly one line;
// anything longer is refused rather than truncated, since a truncated line
// could never verify anyway and silently accepting a prefix would be worse.
const maxUDPDatagram = MaxLineBytes + 1

// Sink records a verified beat. heartbeat.Service implements it.
//
// The contract is deliberately narrow: accepted=false means "answer with
// silence", a non-nil error means an internal fault worth alerting on, and
// NOTHING in the return distinguishes why a beat was refused. That is what
// keeps the listener from becoming an oracle for which orgs and checks exist.
type Sink interface {
	HandleBeat(ctx context.Context, beat *Beat, remoteAddr, transport string) (bool, error)
}

// Server owns the TCP and UDP beat listeners. A zero-value config leaves both
// off, so constructing one is always safe.
type Server struct {
	cfg     config.HeartbeatConfig
	sink    Sink
	limiter *sourceLimiter

	mu       sync.Mutex
	tcp      net.Listener
	udp      *net.UDPConn
	wg       sync.WaitGroup
	conns    chan struct{}
	stopping bool
}

// NewServer builds the push listeners. Nothing binds until Start.
func NewServer(cfg *config.HeartbeatConfig, sink Sink) *Server {
	return &Server{
		cfg:     *cfg,
		sink:    sink,
		limiter: newSourceLimiter(cfg.RatePerMinute, cfg.RateBurst, cfg.ResolvedMaxSourceIPs()),
		conns:   make(chan struct{}, cfg.ResolvedMaxConnections()),
	}
}

// Enabled reports whether either listener is configured.
func (s *Server) Enabled() bool {
	return s.cfg.TCPEnabled() || s.cfg.UDPEnabled()
}

// Start binds the configured listeners and serves them until ctx is canceled
// or Close is called. It returns an error only when a configured listener
// cannot bind — a deployment mistake that must not be silently swallowed.
func (s *Server) Start(ctx context.Context) error {
	if addr := config.NormalizeHeartbeatListen(s.cfg.TCPListen); addr != "" {
		var listenConfig net.ListenConfig

		listener, err := listenConfig.Listen(ctx, "tcp", addr)
		if err != nil {
			return err
		}

		s.mu.Lock()
		s.tcp = listener
		s.mu.Unlock()

		slog.InfoContext(ctx, "Heartbeat push TCP listener started", "address", listener.Addr().String())

		s.wg.Add(1)

		go s.acceptTCP(ctx, listener)
	}

	if addr := config.NormalizeHeartbeatListen(s.cfg.UDPListen); addr != "" {
		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return err
		}

		conn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			return err
		}

		s.mu.Lock()
		s.udp = conn
		s.mu.Unlock()

		slog.InfoContext(ctx, "Heartbeat push UDP listener started", "address", conn.LocalAddr().String())

		s.wg.Add(1)

		go s.serveUDP(ctx, conn)
	}

	return nil
}

// TCPAddr returns the bound TCP address, or nil when the listener is off.
func (s *Server) TCPAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tcp == nil {
		return nil
	}

	return s.tcp.Addr()
}

// UDPAddr returns the bound UDP address, or nil when the listener is off.
func (s *Server) UDPAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.udp == nil {
		return nil
	}

	return s.udp.LocalAddr()
}

// Close stops both listeners and waits for their goroutines to finish.
func (s *Server) Close() error {
	s.mu.Lock()
	s.stopping = true
	tcp, udp := s.tcp, s.udp
	s.tcp, s.udp = nil, nil
	s.mu.Unlock()

	var err error

	if tcp != nil {
		err = errors.Join(err, tcp.Close())
	}

	if udp != nil {
		err = errors.Join(err, udp.Close())
	}

	s.wg.Wait()

	return err
}

// isStopping reports whether Close has been called, so an accept error during
// shutdown is not logged as a fault.
func (s *Server) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.stopping
}

// serveUDP reads one datagram at a time and handles each inline.
//
// Inline, not per-datagram goroutine: a heartbeat is cheap to verify and the
// listener must not be a way to spawn unbounded work from unauthenticated
// input. The per-source budget bounds the rate; the single read loop bounds
// the concurrency.
func (s *Server) serveUDP(ctx context.Context, conn *net.UDPConn) {
	defer s.wg.Done()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, maxUDPDatagram)

	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if s.isStopping() || ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}

			slog.WarnContext(ctx, "Heartbeat push UDP read failed", "error", err)

			continue
		}

		s.handleDatagram(ctx, conn, addr, buf[:n])
	}
}

// handleDatagram processes one UDP datagram.
//
// On ANY failure it replies NOTHING AT ALL — not an error code, not an empty
// packet. A caller must not be able to tell a nonexistent organization from a
// nonexistent check from a bad token from a bad MAC from a replay, because
// each distinction is a free oracle for an unauthenticated attacker.
func (s *Server) handleDatagram(ctx context.Context, conn *net.UDPConn, addr *net.UDPAddr, payload []byte) {
	if !s.limiter.allow(addr) {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportUDP, outcomeRateLimited).Inc()

		return
	}

	if len(payload) > MaxLineBytes {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportUDP, outcomeMalformed).Inc()

		return
	}

	beat, err := ParseLine(payload)
	if err != nil {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportUDP, outcomeMalformed).Inc()

		return
	}

	accepted, err := s.sink.HandleBeat(ctx, beat, sourceKey(addr), TransportUDP)
	if err != nil {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportUDP, outcomeError).Inc()
		slog.ErrorContext(ctx, "Heartbeat push beat failed", "transport", TransportUDP, "error", err)

		return
	}

	if !accepted {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportUDP, outcomeRejected).Inc()

		return
	}

	prommetrics.HeartbeatPushBeats.WithLabelValues(TransportUDP, outcomeAccepted).Inc()

	// Never more bytes out than came in.
	if s.cfg.UDPReplyOK && len(payload) >= len(okReply) {
		_, _ = conn.WriteToUDP([]byte(okReply), addr)
	}
}

// acceptTCP accepts connections until the listener is closed.
func (s *Server) acceptTCP(ctx context.Context, listener net.Listener) {
	defer s.wg.Done()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.isStopping() || ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}

			slog.WarnContext(ctx, "Heartbeat push TCP accept failed", "error", err)

			continue
		}

		select {
		case s.conns <- struct{}{}:
		default:
			// At the connection cap. Close immediately and say nothing.
			prommetrics.HeartbeatPushConnections.WithLabelValues("refused").Inc()
			_ = conn.Close()

			continue
		}

		prommetrics.HeartbeatPushConnections.WithLabelValues("accepted").Inc()

		s.wg.Add(1)

		go func() {
			defer s.wg.Done()
			defer func() { <-s.conns }()

			s.serveConn(ctx, conn)
		}()
	}
}

// serveConn reads newline-delimited beats from one connection.
//
// A device may send one beat and close, or hold the connection open and keep
// beating on it — one handshake total, and the NAT pinhole stays warm, which
// matters on cellular. Guards: an idle deadline, the per-source budget, and a
// bounded line length.
//
// **An open connection is not a heartbeat.** Only an accepted LINE marks the
// check alive, so a hung device holding a socket never reads as up. An invalid
// line closes the connection without a response, which is also what keeps the
// stream from being used to probe for valid tokens.
func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	idle := s.cfg.ResolvedIdleTimeout()
	reader := bufio.NewReaderSize(conn, MaxLineBytes+2)

	for {
		if err := conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return
		}

		line, err := readLine(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) && !s.isStopping() && ctx.Err() == nil {
				slog.DebugContext(ctx, "Heartbeat push TCP connection closed", "reason", err.Error())
			}

			return
		}

		if !s.serveLine(ctx, conn, line) {
			return
		}
	}
}

// serveLine handles one line and reports whether the connection may continue.
func (s *Server) serveLine(ctx context.Context, conn net.Conn, line []byte) bool {
	if !s.limiter.allow(conn.RemoteAddr()) {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportTCP, outcomeRateLimited).Inc()

		return false
	}

	beat, err := ParseLine(line)
	if err != nil {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportTCP, outcomeMalformed).Inc()

		return false
	}

	accepted, err := s.sink.HandleBeat(ctx, beat, sourceKey(conn.RemoteAddr()), TransportTCP)
	if err != nil {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportTCP, outcomeError).Inc()
		slog.ErrorContext(ctx, "Heartbeat push beat failed", "transport", TransportTCP, "error", err)

		return false
	}

	if !accepted {
		prommetrics.HeartbeatPushBeats.WithLabelValues(TransportTCP, outcomeRejected).Inc()

		return false
	}

	prommetrics.HeartbeatPushBeats.WithLabelValues(TransportTCP, outcomeAccepted).Inc()

	if err := writeAll(conn, []byte(okReply+"\n")); err != nil {
		return false
	}

	return true
}

// readLine reads one newline-terminated line, refusing anything longer than
// MaxLineBytes rather than growing a buffer for unauthenticated input.
func readLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, errLineTooLong
	}

	if err != nil {
		return nil, err
	}

	if len(line) > MaxLineBytes+1 {
		return nil, errLineTooLong
	}

	return line[:len(line)-1], nil
}

// writeAll writes the whole buffer under a short deadline, so a peer that
// stops reading cannot pin the connection goroutine forever.
func writeAll(conn net.Conn, payload []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}

	_, err := conn.Write(payload)

	return err
}
