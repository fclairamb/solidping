package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// RequestTimeout returns a bunrouter middleware that aborts a request with
// 504 REQUEST_TIMEOUT when the handler (plus any time it spent in earlier
// middlewares wrapped beneath this one) takes longer than maxDuration.
//
// A maxDuration of 0 disables the middleware: the handler runs unconditionally.
// Paths in excludedPrefixes (workers, heartbeat, /api/mgmt/, /metrics) bypass
// the timeout — long-running worker endpoints and Prometheus scrapes must not
// be capped.
func RequestTimeout(maxDuration time.Duration) func(bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
		return func(writer http.ResponseWriter, req bunrouter.Request) error {
			if maxDuration <= 0 || isExcluded(req.URL.Path) {
				return next(writer, req)
			}

			ctx, cancel := context.WithTimeout(req.Context(), maxDuration)
			defer cancel()

			req.Request = req.Request.WithContext(ctx)
			guarded := &timeoutWriter{ResponseWriter: writer}

			done := make(chan error, 1)
			go func() {
				done <- next(guarded, req)
			}()

			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				// writeTimeout serializes the 504 response with the
				// still-running handler goroutine via the guard's mutex, and
				// only emits it if the handler has not already started writing.
				// This prevents both sides from mutating the shared http.Header
				// map at once ("fatal error: concurrent map writes").
				guarded.writeTimeout()
				// Drain the handler in the background — we cannot stop it,
				// but we have already responded to the client.
				go func() { <-done }()
				return nil
			}
		}
	}
}

// timeoutWriter guards the underlying ResponseWriter so the middleware and the
// handler cannot race on the response. It follows the standard library's
// http.TimeoutHandler pattern: the handler always writes into a *private*
// header map (never the connection's), so a handler that is still running in
// the background after a timeout can keep mutating headers without ever
// touching the same map as the 504 the middleware writes — eliminating the
// "fatal error: concurrent map writes" the shared map otherwise triggers.
//
// The two sides are mutually exclusive: whoever flips the writer first
// (handler via WriteHeader/Write, or the middleware via writeTimeout) wins;
// the loser's subsequent calls become no-ops.
type timeoutWriter struct {
	http.ResponseWriter

	header http.Header // private header map mutated by the handler

	mu       sync.Mutex
	wroteHdr bool // handler has committed its header to the connection
	timedOut bool // the 504 has been (or is being) written
}

// Header returns the handler's private header map. Copied onto the real
// ResponseWriter only when the handler first writes (WriteHeader/Write).
func (w *timeoutWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *timeoutWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timedOut || w.wroteHdr {
		return
	}
	w.wroteHdr = true
	w.flushHeaderLocked()
	w.ResponseWriter.WriteHeader(status)
}

func (w *timeoutWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	if w.timedOut {
		w.mu.Unlock()
		return len(data), nil
	}
	if !w.wroteHdr {
		w.wroteHdr = true
		w.flushHeaderLocked()
	}
	w.mu.Unlock()
	return w.ResponseWriter.Write(data)
}

// flushHeaderLocked copies the handler's private header map onto the real
// response writer. Caller must hold w.mu.
func (w *timeoutWriter) flushHeaderLocked() {
	dst := w.ResponseWriter.Header()
	for k, vv := range w.header {
		dst[k] = vv
	}
}

// writeTimeout emits the 504 response, but only if the handler has not already
// committed its own header to the connection. It runs under the same mutex as
// the handler's WriteHeader/Write, and writes only to the real ResponseWriter's
// header map (never the handler's private one), so the two never collide.
func (w *timeoutWriter) writeTimeout() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wroteHdr || w.timedOut {
		return
	}
	w.timedOut = true

	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	w.ResponseWriter.WriteHeader(http.StatusGatewayTimeout)
	body, err := json.Marshal(base.ErrorResponse{
		Title:  "Request timed out",
		Code:   string(base.ErrorCodeRequestTimeout),
		Detail: "The server took too long to produce a response",
	})
	if err == nil {
		_, _ = w.ResponseWriter.Write(body)
	}
}
