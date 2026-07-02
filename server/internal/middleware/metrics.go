package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// statusRecorder wraps http.ResponseWriter to capture the response status
// code for metrics recording. Defaults to 200 since net/http auto-writes
// 200 if no WriteHeader call happens before the first Write.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader records the status code and forwards to the underlying writer.
func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wroteHeader {
		sr.status = code
		sr.wroteHeader = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

// Write defaults the status to 200 if WriteHeader was not called first,
// matching the net/http behavior.
func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.status = http.StatusOK
		sr.wroteHeader = true
	}
	return sr.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer when it supports flushing, so
// streaming handlers (SSE) keep working through the metrics wrapper.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer for http.ResponseController, which
// walks Unwrap chains to find Flush/deadline support.
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// HTTPMetrics is a bunrouter middleware that records per-route HTTP
// duration and request-count metrics. Routes are taken from the matched
// pattern via bunrouter.Request.Route() so cardinality stays bounded
// (one series per registered route, not per raw URL path).
func HTTPMetrics(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}

		err := next(rec, req)

		// Some handlers return an error without writing a status; treat that
		// as 500 so the histogram doesn't lie. The error itself is logged
		// upstream by the logging middleware.
		status := rec.status
		if err != nil && !rec.wroteHeader {
			status = http.StatusInternalServerError
		}

		prommetrics.RecordHTTPRequest(
			req.Method,
			req.Route(),
			strconv.Itoa(status),
			time.Since(start).Seconds(),
		)

		return err
	}
}
