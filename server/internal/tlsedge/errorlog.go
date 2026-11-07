package tlsedge

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/netip"
	"strings"
)

// classify returns a constant label for a net/http error line.
func classify(text string) string {
	reasons := []struct{ match, label string }{
		{ErrHostNotAllowed.Error(), "hostname not allowed"},
		{"client sent an HTTP request to an HTTPS server", "plain HTTP request on TLS port"},
		{"first record does not look like a TLS handshake", "not a TLS handshake"},
		{"no cipher suite supported by both client and server", "no common cipher suite"},
		{"client offered only unsupported versions", "unsupported TLS version"},
		{"unsupported SSLv2 handshake received", "unsupported SSLv2 handshake"},
		{"tls: unknown certificate", "client rejected certificate"},
		{"tls: bad certificate", "client rejected certificate"},
		{"certificate", "certificate error"},
		{"i/o timeout", "i/o timeout"},
		{"connection reset by peer", "connection reset by peer"},
		{"EOF", "EOF"},
		{"Accept error", "accept error"},
		{"panic serving", "handler panic"},
		{"superfluous response.WriteHeader", "superfluous WriteHeader"},
	}

	for idx := range reasons {
		if strings.Contains(text, reasons[idx].match) {
			return reasons[idx].label
		}
	}

	return "other"
}

const handshakePrefix = "http: TLS handshake error from "

// errorLogWriter turns the lines net/http writes through its stdlib ErrorLog
// into structured slog records. Without it they surface at INFO with the
// client address and host inside the message, which makes them ungroupable.
type errorLogWriter struct {
	log *slog.Logger
}

// newServerErrorLog builds the *log.Logger to set as http.Server.ErrorLog.
func newServerErrorLog(logger *slog.Logger) *log.Logger {
	return log.New(&errorLogWriter{log: logger}, "", 0)
}

// Write implements io.Writer; one call is one log line.
func (w *errorLogWriter) Write(line []byte) (int, error) {
	text := string(bytes.TrimRight(line, "\r\n"))
	ctx := context.Background()

	rest, ok := strings.CutPrefix(text, handshakePrefix)
	if !ok {
		w.log.WarnContext(ctx, "HTTP server error", "reason", classify(text))

		return len(line), nil
	}

	remote, errText, _ := strings.Cut(rest, ": ")
	attrs := []any{"reason", classify(errText)}

	if addr, err := netip.ParseAddrPort(remote); err == nil {
		attrs = append(attrs, "remote_addr", addr.String())
	}

	w.log.DebugContext(ctx, "TLS handshake error", attrs...)

	return len(line), nil
}
