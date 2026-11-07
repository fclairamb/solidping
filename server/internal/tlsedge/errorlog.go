package tlsedge

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"strings"
)

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
		w.log.WarnContext(ctx, "HTTP server error", "error", text)

		return len(line), nil
	}

	remote, errText, _ := strings.Cut(rest, ": ")
	attrs := []any{"remote_addr", remote, "error", errText}

	if _, host, found := strings.Cut(errText, ErrHostNotAllowed.Error()+": "); found {
		attrs = append(attrs, "sni", host)
	}

	w.log.DebugContext(ctx, "TLS handshake error", attrs...)

	return len(line), nil
}
