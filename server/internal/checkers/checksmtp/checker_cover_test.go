package checksmtp

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTLSVersionString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version uint16
		want    string
	}{
		{tls.VersionTLS10, "TLS 1.0"},
		{tls.VersionTLS11, "TLS 1.1"},
		{tls.VersionTLS12, "TLS 1.2"},
		{tls.VersionTLS13, "TLS 1.3"},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, tlsVersionString(tt.version))
	}

	require.NotEqual(t, "TLS 1.2", tlsVersionString(0x0001))
}

func TestAliasValidators(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateMailFrom("alice@acme.com"))
	require.Error(t, ValidateMailFrom("alice@acme.com\r\nRCPT TO:<x@y>"))
	require.NoError(t, ValidateDeliveryTo("bob@acme.com"))
	require.Error(t, ValidateDeliveryTo("bob@acme.com>\r\nDATA"))
}

// pipeServer runs handler on the server side of a net.Pipe and returns a
// textproto.Conn plus the raw client conn.
func pipeServer(t *testing.T, handler func(r *bufio.Reader, w net.Conn)) (*textproto.Conn, net.Conn) {
	t.Helper()

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	go handler(bufio.NewReader(server), server)

	return textproto.NewConn(client), client
}

func TestDoAUTH(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reply   string
		wantErr string
	}{
		{"accepted", "235 2.7.0 ok\r\n", ""},
		{"rejected", "535 5.7.8 bad credentials\r\n", "authentication failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn, _ := pipeServer(t, func(r *bufio.Reader, w net.Conn) {
				line, _ := r.ReadString('\n')
				if !strings.HasPrefix(line, "AUTH PLAIN ") {
					return
				}

				_, _ = w.Write([]byte(tt.reply))
			})

			err := (&SMTPChecker{}).doAUTH(conn, "user", "pass")
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestDoAUTHWriteError(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	_ = server.Close()
	_ = client.Close()

	err := (&SMTPChecker{}).doAUTH(textproto.NewConn(client), "u", "p")
	require.ErrorContains(t, err, "failed to send AUTH")
}

func TestDoSTARTTLSErrors(t *testing.T) {
	t.Parallel()

	c := &SMTPChecker{}

	// Not advertised.
	_, err := c.doSTARTTLS(context.Background(), nil, nil, "h", false, ehloCapabilities{})
	require.ErrorIs(t, err, errSTARTTLSNotAdvertised)

	// Rejected by the server.
	conn, raw := pipeServer(t, func(r *bufio.Reader, w net.Conn) {
		_, _ = r.ReadString('\n')
		_, _ = w.Write([]byte("454 TLS not available\r\n"))
	})

	_, err = c.doSTARTTLS(context.Background(), conn, raw, "h", false, ehloCapabilities{hasStartTLS: true})
	require.ErrorContains(t, err, "STARTTLS rejected")

	// Accepted, but the handshake fails because the peer sends garbage.
	conn, raw = pipeServer(t, func(r *bufio.Reader, w net.Conn) {
		_, _ = r.ReadString('\n')
		_, _ = w.Write([]byte("220 go ahead\r\n"))
		go func() { _, _ = io.Copy(io.Discard, r) }()

		_, _ = w.Write([]byte("this is not tls\r\n"))
		_ = w.Close()
	})

	_, err = c.doSTARTTLS(context.Background(), conn, raw, "h", false, ehloCapabilities{hasStartTLS: true})
	require.ErrorContains(t, err, "STARTTLS handshake failed")
}
