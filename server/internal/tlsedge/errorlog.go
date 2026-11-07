package tlsedge

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/netip"
	"strings"
	"unicode"
)

const (
	maxLoggedText = 256
	maxSNILen     = 253
)

// sanitize keeps printable runes only and bounds the length, so a log line can
// never carry control characters or unbounded text from the writer.
func sanitize(text string, limit int) string {
	var out strings.Builder

	count := 0

	for _, r := range text {
		if count >= limit {
			break
		}

		if unicode.IsPrint(r) {
			out.WriteRune(r)

			count++
		}
	}

	return out.String()
}

// validHostname reports whether name looks like a DNS name (letters, digits, dots, dashes).
func validHostname(name string) bool {
	if name == "" || len(name) > maxSNILen {
		return false
	}

	for _, r := range name {
		isAlnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !isAlnum && r != '.' && r != '-' && r != '_' {
			return false
		}
	}

	return true
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
		w.log.WarnContext(ctx, "HTTP server error", "error", sanitize(text, maxLoggedText))

		return len(line), nil
	}

	remote, errText, _ := strings.Cut(rest, ": ")
	attrs := []any{"error", sanitize(errText, maxLoggedText)}

	if addr, err := netip.ParseAddrPort(remote); err == nil {
		attrs = append(attrs, "remote_addr", addr.String())
	}

	if _, host, found := strings.Cut(errText, ErrHostNotAllowed.Error()+": "); found && validHostname(host) {
		attrs = append(attrs, "sni", host)
	}

	w.log.DebugContext(ctx, "TLS handshake error", attrs...)

	return len(line), nil
}
