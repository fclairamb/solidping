package tlsedge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// httpHeadSizeLimit caps how much of a plaintext request head the Host peek is
// willing to buffer. A legitimate request line plus headers is well under this;
// anything larger is either an attack or a request this edge has no business
// routing, and is kept local rather than forwarded.
const httpHeadSizeLimit = 8 << 10

// httpPeekChunkSize is the read granularity of the Host peek. Small enough that
// a fragmented head costs a few syscalls, large enough that the common
// single-segment case is one read.
const httpPeekChunkSize = 1024

// errClientHelloPeeked aborts the TLS handshake from inside GetConfigForClient,
// the moment the ClientHello has been parsed. It is the signal that the peek
// succeeded — NOT a failure — and it guarantees no key exchange, no certificate
// selection and no termination ever happen on a connection that may belong to
// another instance.
var errClientHelloPeeked = errors.New("tlsedge: client hello peeked")

// errHTTPHeadTooLarge is returned when a plaintext request head exceeds
// httpHeadSizeLimit before its terminating blank line.
var errHTTPHeadTooLarge = errors.New("tlsedge: request head exceeds the peek limit")

// errNoHostHeader is returned when a plaintext request carries no Host.
var errNoHostHeader = errors.New("tlsedge: request has no Host header")

// hostPeeker reads just enough of a freshly accepted connection to learn which
// hostname it is for. It returns the normalized host (empty when the protocol
// carries none), EVERY byte it consumed so the connection can be replayed
// intact, and an error when the head could not be read or parsed.
//
// The contract callers rely on: the buffered bytes are always complete, error
// or not — a failed peek must still be replayable to the local stack, because
// a connection whose host is unknown is never forwarded.
type hostPeeker func(conn net.Conn, timeout time.Duration) (string, []byte, error)

// peekTLSClientHello extracts the SNI from a TLS ClientHello without
// terminating anything. crypto/tls does the parsing (fragmented records,
// extension layout, version quirks included) and is aborted from
// GetConfigForClient as soon as the hello is available, before any response is
// generated; recordingConn keeps every byte crypto/tls consumed and swallows
// the alert it tries to write on abort, so the client sees an untouched
// connection.
//
// A hello with no SNI yields ("", buffered, nil): not an error, just a
// connection the splitter must keep local.
func peekTLSClientHello(conn net.Conn, timeout time.Duration) (string, []byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", nil, fmt.Errorf("tlsedge: cannot set the ClientHello read deadline: %w", err)
	}

	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	recorder := &recordingConn{Conn: conn}

	var serverName string

	server := tls.Server(recorder, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			serverName = hello.ServerName

			return nil, errClientHelloPeeked
		},
	})

	// Background, deliberately: a context with a Done channel makes
	// HandshakeContext close the underlying connection on cancellation, which
	// would kill a connection we are about to hand on. The read deadline above
	// is the timeout.
	err := server.HandshakeContext(context.Background())

	buffered := recorder.buffered()

	if err != nil && !errors.Is(err, errClientHelloPeeked) {
		return "", buffered, fmt.Errorf("tlsedge: cannot read the TLS ClientHello: %w", err)
	}

	return normalizeHost(serverName), buffered, nil
}

// peekHTTPHost extracts the Host of a plaintext request without consuming it.
// It buffers up to the head's terminating blank line (capped at
// httpHeadSizeLimit), parses that with net/http, and returns everything it read
// for replay.
func peekHTTPHost(conn net.Conn, timeout time.Duration) (string, []byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", nil, fmt.Errorf("tlsedge: cannot set the request head read deadline: %w", err)
	}

	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	head, err := readRequestHead(conn)
	if err != nil {
		return "", head, err
	}

	request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(head)))
	if err != nil {
		return "", head, fmt.Errorf("tlsedge: cannot parse the request head: %w", err)
	}

	host := normalizeHost(request.Host)
	if host == "" {
		return "", head, errNoHostHeader
	}

	return host, head, nil
}

// readRequestHead reads until the blank line that ends a request head, returning
// everything consumed. An oversized head fails rather than being truncated: a
// truncated head could parse to the wrong Host, and a wrong Host is a
// misrouted connection.
func readRequestHead(conn net.Conn) ([]byte, error) {
	var (
		head  []byte
		chunk = make([]byte, httpPeekChunkSize)
	)

	for {
		read, err := conn.Read(chunk)
		if read > 0 {
			head = append(head, chunk[:read]...)

			if bytes.Contains(head, []byte("\r\n\r\n")) {
				return head, nil
			}

			if len(head) >= httpHeadSizeLimit {
				return head, errHTTPHeadTooLarge
			}
		}

		if err != nil {
			return head, fmt.Errorf("tlsedge: cannot read the request head: %w", err)
		}
	}
}

// recordingConn is the peek's window onto a connection: it keeps a copy of
// everything read and drops everything written. Dropping writes is what makes
// the peek non-destructive — crypto/tls emits an alert record when
// GetConfigForClient returns an error, and sending that would tell a client
// whose connection we are about to forward that TLS failed here.
type recordingConn struct {
	net.Conn

	buf bytes.Buffer
}

func (c *recordingConn) Read(p []byte) (int, error) {
	read, err := c.Conn.Read(p)
	if read > 0 {
		c.buf.Write(p[:read])
	}

	return read, err //nolint:wrapcheck // transparent pass-through of the peer's read error
}

// Write swallows the payload and reports success, so the peek can never emit a
// byte on the wire.
func (c *recordingConn) Write(p []byte) (int, error) {
	return len(p), nil
}

// buffered returns every byte read so far.
func (c *recordingConn) buffered() []byte {
	return c.buf.Bytes()
}

// prefixConn replays a peeked prefix ahead of the live connection, so the local
// stack (crypto/tls or net/http) reads a byte-for-byte identical stream. Only
// Read is overridden: RemoteAddr, deadlines and Close keep coming from the
// wrapped connection, which is what net/http keys request logging and rate
// limiting on.
type prefixConn struct {
	net.Conn

	reader io.Reader
}

// newPrefixConn wraps conn so prefix is read first. An empty prefix returns the
// connection untouched.
func newPrefixConn(conn net.Conn, prefix []byte) net.Conn {
	if len(prefix) == 0 {
		return conn
	}

	return &prefixConn{
		Conn:   conn,
		reader: io.MultiReader(bytes.NewReader(prefix), conn),
	}
}

func (c *prefixConn) Read(p []byte) (int, error) {
	return c.reader.Read(p) //nolint:wrapcheck // transparent pass-through of the peer's read error
}
