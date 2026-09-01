package heartbeatpush_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
)

// fakeSink records what reached the ingest and answers according to a
// programmable verdict, so the listener's wire behavior can be tested without
// a database.
type fakeSink struct {
	mu       sync.Mutex
	beats    []*heartbeatpush.Beat
	accept   bool
	failWith error
}

func (f *fakeSink) HandleBeat(
	_ context.Context, beat *heartbeatpush.Beat, _, _ string,
) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.beats = append(f.beats, beat)

	if f.failWith != nil {
		return false, f.failWith
	}

	return f.accept, nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.beats)
}

// startServer boots both listeners on ephemeral ports.
func startServer(t *testing.T, sink heartbeatpush.Sink, mutate func(*config.HeartbeatConfig)) *heartbeatpush.Server {
	t.Helper()

	cfg := config.DefaultHeartbeatConfig()
	cfg.TCPListen = "127.0.0.1:0"
	cfg.UDPListen = "127.0.0.1:0"

	if mutate != nil {
		mutate(&cfg)
	}

	server := heartbeatpush.NewServer(&cfg, sink)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Close() })

	return server
}

// sendDatagram sends one payload and waits briefly for a reply, returning the
// reply bytes and whether one arrived at all.
func sendDatagram(t *testing.T, addr net.Addr, payload string) ([]byte, bool) {
	t.Helper()

	udpAddr, ok := addr.(*net.UDPAddr)
	require.True(t, ok)

	conn, err := net.DialUDP("udp", nil, udpAddr)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	_, err = conn.Write([]byte(payload))
	require.NoError(t, err)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(300*time.Millisecond)))

	buf := make([]byte, 512)

	n, err := conn.Read(buf)
	if err != nil {
		return nil, false
	}

	return buf[:n], true
}

const validLine = "SP1 acme/sensor-1 0123456789abcdef0123456789abcdef"

// TestUDPRepliesOKOnlyWhenAccepted is the positive control for every silence
// assertion below.
func TestUDPRepliesOKOnlyWhenAccepted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, nil)

	reply, gotReply := sendDatagram(t, server.UDPAddr(), validLine)
	r.True(gotReply)
	r.Equal("OK", string(reply))

	// The reply can never be longer than the datagram that triggered it, so
	// the listener is not an amplification vector.
	r.Less(len(reply), len(validLine))
}

// TestUDPIsSilentOnEveryFailure is the no-oracle property: a caller must not
// be able to tell an unknown org from an unknown check from a bad token from a
// bad MAC from a replay — every one of them is byte-identical silence.
func TestUDPIsSilentOnEveryFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rejecting := &fakeSink{accept: false}
	server := startServer(t, rejecting, nil)

	for _, payload := range []string{
		validLine, // valid syntax, refused by the ingest
		"SP1 no-such-org/sensor-1 0123456789abcdef",       // unknown organization
		"SP1 acme/no-such-check 0123456789abcdef",         // unknown check
		"SP1 acme/sensor-1 wrong-token",                   // wrong token
		heartbeatpush.SignSP2("acme", "s", "k", 0, 1, ""), // bad MAC as far as the ingest is concerned
		"garbage", // malformed
		"",        // empty
	} {
		_, gotReply := sendDatagram(t, server.UDPAddr(), payload)
		r.False(gotReply, "a failure must produce no bytes at all: %q", payload)
	}

	// Positive control on the same socket: an accepted beat DOES answer, so
	// the silences above are not simply a dead listener.
	accepting := &fakeSink{accept: true}
	okServer := startServer(t, accepting, nil)
	_, gotReply := sendDatagram(t, okServer.UDPAddr(), validLine)
	r.True(gotReply)
}

// TestUDPDoesNotReplyWhenDisabled covers the operator switch.
func TestUDPDoesNotReplyWhenDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, func(cfg *config.HeartbeatConfig) { cfg.UDPReplyOK = false })

	_, gotReply := sendDatagram(t, server.UDPAddr(), validLine)
	r.False(gotReply)

	// The beat was still ingested — only the reply is suppressed.
	r.Eventually(func() bool { return sink.count() == 1 }, time.Second, 10*time.Millisecond)
}

// TestUDPRejectsOversizedDatagramWithoutParsing bounds what one datagram costs.
func TestUDPRejectsOversizedDatagramWithoutParsing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, nil)

	oversized := "SP1 acme/sensor-1 "
	for len(oversized) <= heartbeatpush.MaxLineBytes {
		oversized += "0123456789"
	}

	_, gotReply := sendDatagram(t, server.UDPAddr(), oversized)
	r.False(gotReply)
	r.Zero(sink.count(), "an oversized datagram must never reach the ingest")
}

// dialTCP opens a connection to the beat listener.
func dialTCP(t *testing.T, addr net.Addr) (net.Conn, *bufio.Reader) {
	t.Helper()

	dialer := net.Dialer{Timeout: time.Second}

	conn, err := dialer.DialContext(t.Context(), "tcp", addr.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return conn, bufio.NewReader(conn)
}

// TestTCPAnswersEachAcceptedLine covers connection reuse: one handshake, many
// beats, one OK per accepted line.
func TestTCPAnswersEachAcceptedLine(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, nil)

	conn, reader := dialTCP(t, server.TCPAddr())

	for range 3 {
		_, err := conn.Write([]byte(validLine + "\n"))
		r.NoError(err)

		r.NoError(conn.SetReadDeadline(time.Now().Add(time.Second)))

		line, err := reader.ReadString('\n')
		r.NoError(err)
		r.Equal("OK\n", line)
	}

	r.Equal(3, sink.count())
}

// TestTCPOpenConnectionIsNotAHeartbeat is the load-bearing liveness rule: a
// hung device holding a socket open must never read as up. Only an accepted
// LINE marks the check alive.
func TestTCPOpenConnectionIsNotAHeartbeat(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, nil)

	conn, _ := dialTCP(t, server.TCPAddr())
	r.NotNil(conn)

	// Hold it open, send nothing.
	time.Sleep(200 * time.Millisecond)
	r.Zero(sink.count(), "an open connection must not record a beat")

	// A partial line — no newline yet — is not a beat either.
	_, err := conn.Write([]byte(validLine))
	r.NoError(err)
	time.Sleep(200 * time.Millisecond)
	r.Zero(sink.count(), "an unterminated line must not record a beat")

	// Positive control: completing the line does record one.
	_, err = conn.Write([]byte("\n"))
	r.NoError(err)
	r.Eventually(func() bool { return sink.count() == 1 }, time.Second, 10*time.Millisecond)
}

// TestTCPClosesOnAnInvalidLineWithoutAnswering — an invalid line closes the
// connection with no response, so the stream cannot be used to probe tokens.
func TestTCPClosesOnAnInvalidLineWithoutAnswering(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: false}
	server := startServer(t, sink, nil)

	conn, reader := dialTCP(t, server.TCPAddr())

	_, err := conn.Write([]byte(validLine + "\n"))
	r.NoError(err)

	r.NoError(conn.SetReadDeadline(time.Now().Add(time.Second)))

	_, err = reader.ReadString('\n')
	r.Error(err, "a refused line must close the connection without a response")
}

// TestTCPRefusesAnOverlongLine bounds unauthenticated input.
func TestTCPRefusesAnOverlongLine(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, nil)

	conn, reader := dialTCP(t, server.TCPAddr())

	payload := make([]byte, heartbeatpush.MaxLineBytes*4)
	for i := range payload {
		payload[i] = 'a'
	}

	_, _ = conn.Write(payload)

	r.NoError(conn.SetReadDeadline(time.Now().Add(time.Second)))

	_, err := reader.ReadString('\n')
	r.Error(err)
	r.Zero(sink.count())
}

// TestTCPIdleTimeoutClosesTheConnection proves a device that stops beating
// releases its slot rather than pinning it forever.
func TestTCPIdleTimeoutClosesTheConnection(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, func(cfg *config.HeartbeatConfig) {
		cfg.IdleTimeout = 150 * time.Millisecond
	})

	conn, reader := dialTCP(t, server.TCPAddr())
	r.NotNil(conn)

	r.NoError(conn.SetReadDeadline(time.Now().Add(2 * time.Second)))

	_, err := reader.ReadString('\n')
	r.Error(err, "the server must have closed the idle connection")
}

// TestRateLimitDropsBeyondTheBudget proves the per-source budget applies and
// that an over-budget beat never reaches the ingest.
func TestRateLimitDropsBeyondTheBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &fakeSink{accept: true}
	server := startServer(t, sink, func(cfg *config.HeartbeatConfig) {
		cfg.RatePerMinute = 60
		cfg.RateBurst = 2
	})

	// Fire without waiting for replies: waiting would stretch the flood over
	// seconds and let the bucket refill, which is exactly what made this
	// assertion meaningless on the first attempt.
	udpAddr, ok := server.UDPAddr().(*net.UDPAddr)
	r.True(ok)

	conn, err := net.DialUDP("udp", nil, udpAddr)
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	for range 50 {
		_, writeErr := conn.Write([]byte(validLine))
		r.NoError(writeErr)
	}

	time.Sleep(300 * time.Millisecond)

	r.LessOrEqual(sink.count(), 5, "the budget must drop the flood before the ingest sees it")
	r.Positive(sink.count(), "positive control: the first beats do get through")
}

// TestListenersOffByDefault — a zero-value configuration binds nothing.
func TestListenersOffByDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.DefaultHeartbeatConfig()
	server := heartbeatpush.NewServer(&cfg, &fakeSink{accept: true})
	r.False(server.Enabled())
	r.NoError(server.Start(t.Context()))
	r.Nil(server.TCPAddr())
	r.Nil(server.UDPAddr())
	r.NoError(server.Close())
}

// TestStartFailsOnABadAddress — a listener that cannot bind is a deployment
// mistake and must not be swallowed.
func TestStartFailsOnABadAddress(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.DefaultHeartbeatConfig()
	cfg.TCPListen = "256.256.256.256:1"

	server := heartbeatpush.NewServer(&cfg, &fakeSink{accept: true})
	err := server.Start(t.Context())
	r.Error(err)
	r.NoError(server.Close())

	var opErr *net.OpError
	r.True(errors.As(err, &opErr) || err != nil)
}
