package tlsedge

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// peekTestTimeout is the deadline handed to the peek in the cases that are not
// about timing out.
const peekTestTimeout = 5 * time.Second

// TestPeekTLSClientHello is the core claim of the HTTPS splitter: the SNI can
// be read off a connection without terminating it, whatever shape the hello
// arrives in, and every byte consumed comes back for replay. Getting this wrong
// is not a degraded feature — it is a client whose handshake we silently broke.
func TestPeekTLSClientHello(t *testing.T) {
	t.Parallel()

	const sni = "downstream.example.com"

	tests := []struct {
		name string
		// chunk is the number of bytes per TCP write; 0 means "all at once".
		chunk int
		// serverName is the SNI the generated hello asks for; empty generates a
		// hello with no SNI extension at all.
		serverName string
		wantHost   string
	}{
		{name: "a hello delivered whole", serverName: sni, wantHost: sni},
		{name: "a hello fragmented one byte at a time", chunk: 1, serverName: sni, wantHost: sni},
		{name: "a hello fragmented across a few segments", chunk: 40, serverName: sni, wantHost: sni},
		{
			// Health probes, IP scans and TLS-ALPN-01 from an old client all
			// look like this. There is no host to route on, so the connection
			// must stay local — never forwarded on a guess.
			name: "a hello with no SNI yields no host and no error", serverName: "", wantHost: "",
		},
		{
			name:       "an uppercase SNI is normalized",
			serverName: "DownStream.Example.COM", wantHost: sni,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			hello := clientHelloBytes(t, tc.serverName)
			conn := scriptedConn(t, hello, tc.chunk)

			host, buffered, err := peekTLSClientHello(conn, peekTestTimeout)
			r.NoError(err)
			r.Equal(tc.wantHost, host)
			r.Equal(hello, buffered,
				"every peeked byte must come back for replay, or the handshake below us breaks")
		})
	}
}

// TestPeekTLSClientHelloFailures pins the fail-local half of the contract: a
// peek that cannot produce a host returns an error AND the bytes it consumed,
// so the splitter can still hand the connection to the local stack. A failure
// here must never look like "no SNI, carry on".
func TestPeekTLSClientHelloFailures(t *testing.T) {
	t.Parallel()

	t.Run("garbage is an error, not an empty host", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		garbage := []byte("this is not a TLS record at all, not even close\r\n\r\n")
		conn := scriptedConn(t, garbage, 0)

		host, buffered, err := peekTLSClientHello(conn, peekTestTimeout)
		r.Error(err)
		r.Empty(host)
		r.NotEmpty(buffered, "the bytes read must still be replayable to the local stack")
	})

	t.Run("a silent client times out instead of hanging", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		_, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })

		start := time.Now()
		host, _, err := peekTLSClientHello(server, 150*time.Millisecond)
		r.Error(err)
		r.Empty(host)
		r.Less(time.Since(start), 3*time.Second, "the peek must be bounded by its deadline")
	})

	t.Run("an immediately closed connection is an error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		client, server := net.Pipe()
		r.NoError(client.Close())

		host, _, err := peekTLSClientHello(server, peekTestTimeout)
		r.Error(err)
		r.Empty(host)
	})
}

// TestPeekTLSClientHelloWritesNothing is the "no TLS termination before the
// decision" guarantee, observed from the client side: crypto/tls wants to send
// an alert when the peek aborts the handshake, and if that alert reached the
// wire a connection we are about to forward would already have been told TLS
// failed here.
func TestPeekTLSClientHelloWritesNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	hello := clientHelloBytes(t, "downstream.example.com")

	client, server := net.Pipe()

	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	go func() { _, _ = client.Write(hello) }()

	host, _, err := peekTLSClientHello(server, peekTestTimeout)
	r.NoError(err)
	r.Equal("downstream.example.com", host)

	// Nothing may come back on the client side — not even an alert.
	r.NoError(client.SetReadDeadline(time.Now().Add(200 * time.Millisecond)))

	buf := make([]byte, 64)
	read, readErr := client.Read(buf)
	r.Zero(read, "the peek must not write a single byte to the client")
	r.Error(readErr)
}

// TestPeekHTTPHost covers the :80 splitter's decision input. The oversized case
// is the security-relevant one: a head that never ends must not be truncated
// into a parseable-but-wrong Host, which would misroute the connection.
func TestPeekHTTPHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  string
		chunk    int
		wantHost string
		wantErr  error
	}{
		{
			name:     "a normal request",
			request:  "GET /.well-known/acme-challenge/token HTTP/1.1\r\nHost: downstream.example.com\r\n\r\n",
			wantHost: "downstream.example.com",
		},
		{
			name:     "a head fragmented one byte at a time",
			request:  "GET / HTTP/1.1\r\nHost: downstream.example.com\r\nUser-Agent: probe\r\n\r\n",
			chunk:    1,
			wantHost: "downstream.example.com",
		},
		{
			name:     "a Host with a port and mixed case is normalized",
			request:  "GET / HTTP/1.1\r\nHost: DownStream.Example.com:8080\r\n\r\n",
			wantHost: "downstream.example.com",
		},
		{
			name:     "an HTTP/1.0 request with no Host has nothing to route on",
			request:  "GET / HTTP/1.0\r\n\r\n",
			wantHost: "",
			wantErr:  errNoHostHeader,
		},
		{
			name:    "a head larger than the cap is refused, not truncated",
			request: "GET / HTTP/1.1\r\nHost: downstream.example.com\r\nX-Pad: " + strings.Repeat("a", httpHeadSizeLimit),
			wantErr: errHTTPHeadTooLarge,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			conn := scriptedConn(t, []byte(tc.request), tc.chunk)

			host, buffered, err := peekHTTPHost(conn, peekTestTimeout)
			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)
				r.Empty(host)
				r.NotEmpty(buffered, "even a refused head must be replayable to the local stack")

				return
			}

			r.NoError(err)
			r.Equal(tc.wantHost, host)
			r.Equal(tc.request, string(buffered))
		})
	}
}

// TestPeekHTTPHostFailures pins the remaining fail-local paths of the :80 peek.
func TestPeekHTTPHostFailures(t *testing.T) {
	t.Parallel()

	t.Run("garbage is an error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		conn := scriptedConn(t, []byte("\x00\x01\x02 not http at all\r\n\r\n"), 0)

		host, buffered, err := peekHTTPHost(conn, peekTestTimeout)
		r.Error(err)
		r.Empty(host)
		r.NotEmpty(buffered)
	})

	t.Run("a silent client times out instead of hanging", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		_, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })

		start := time.Now()
		host, _, err := peekHTTPHost(server, 150*time.Millisecond)
		r.Error(err)
		r.Empty(host)
		r.Less(time.Since(start), 3*time.Second)
	})
}

// TestPrefixConnReplaysThenContinues proves the replay wrapper hands the local
// stack a byte-for-byte identical stream: the peeked prefix first, then the
// live connection, with no boundary visible to the reader.
func TestPrefixConnReplaysThenContinues(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client, server := net.Pipe()

	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_, _ = client.Write([]byte("-rest-of-the-stream"))
		_ = client.Close()
	}()

	replayed := newPrefixConn(server, []byte("peeked-prefix"))

	all, err := io.ReadAll(replayed)
	r.NoError(err)
	r.Equal("peeked-prefix-rest-of-the-stream", string(all))

	// An empty prefix must not even allocate a wrapper: the local stack should
	// see the original connection.
	r.Equal(server, newPrefixConn(server, nil))
}

// clientHelloBytes captures the raw ClientHello a real crypto/tls client sends
// for the given SNI (empty = no SNI extension), so the peek is exercised
// against genuine protocol bytes rather than a hand-rolled record.
func clientHelloBytes(t *testing.T, serverName string) []byte {
	t.Helper()
	r := require.New(t)

	client, server := net.Pipe()

	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	go func() {
		// The handshake never completes — nothing answers — which is fine: the
		// hello is written before the first read.
		_ = tls.Client(client, &tls.Config{
			ServerName: serverName,
			// Only reached if a server ever answered; it never does here.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		}).HandshakeContext(context.Background())
	}()

	r.NoError(server.SetReadDeadline(time.Now().Add(peekTestTimeout)))

	buf := make([]byte, 4096)

	read, err := server.Read(buf)
	r.NoError(err)
	r.NotZero(read)

	hello := bytes.Clone(buf[:read])

	if serverName == "" {
		r.NotContains(string(hello), "example.com", "the no-SNI case must really carry no name")
	}

	return hello
}

// scriptedConn returns a connection that delivers data in chunk-sized writes
// (0 = one write), which is how a fragmented ClientHello or request head is
// reproduced deterministically. net.Pipe is synchronous, so each write is
// consumed by the reader before the next one starts.
func scriptedConn(t *testing.T, data []byte, chunk int) net.Conn {
	t.Helper()

	client, server := net.Pipe()

	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	if chunk <= 0 {
		chunk = len(data)
	}

	go func() {
		for start := 0; start < len(data); start += chunk {
			end := min(start+chunk, len(data))

			if _, err := client.Write(data[start:end]); err != nil {
				return
			}
		}
		// Deliberately left open: a peek must finish on the head alone, not on
		// EOF.
	}()

	return server
}
