package tlsedge

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrorLogWriter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := newServerErrorLog(logger)

	l.Printf("http: TLS handshake error from 203.0.113.7:4242: %s: scanner.acme.com", ErrHostNotAllowed)

	out := buf.String()
	require.Contains(t, out, "level=DEBUG")
	require.Contains(t, out, `msg="TLS handshake error"`)
	require.Contains(t, out, "remote_addr=203.0.113.7:4242")
	require.Contains(t, out, "sni=scanner.acme.com")
	require.NotContains(t, out, "msg=\"http:")

	buf.Reset()
	l.Printf("http: Accept error: boom; retrying in 5ms")

	out = buf.String()
	require.Contains(t, out, "level=WARN")
	require.Contains(t, out, `msg="HTTP server error"`)
	require.Contains(t, out, `error="http: Accept error: boom; retrying in 5ms"`)
}
