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
			tw := &timeoutWriter{ResponseWriter: writer}

			done := make(chan error, 1)
			go func() {
				done <- next(tw, req)
			}()

			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				if tw.tryClaim() {
					writeTimeoutError(writer)
				}
				// Drain the handler in the background — we cannot stop it,
				// but we have already responded to the client.
				go func() { <-done }()
				return nil
			}
		}
	}
}

// timeoutWriter guards the underlying ResponseWriter so the middleware and the
// handler cannot race on the first WriteHeader/Write. Whoever calls tryClaim()
// (or implicitly via the first Write*) first wins; the other becomes a no-op.
type timeoutWriter struct {
	http.ResponseWriter

	mu      sync.Mutex
	claimed bool
}

func (w *timeoutWriter) tryClaim() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.claimed {
		return false
	}
	w.claimed = true
	return true
}

func (w *timeoutWriter) WriteHeader(status int) {
	w.mu.Lock()
	if w.claimed {
		w.mu.Unlock()
		return
	}
	w.claimed = true
	w.mu.Unlock()
	w.ResponseWriter.WriteHeader(status)
}

func (w *timeoutWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	if w.claimed {
		// Handler already lost the race; swallow the write so it doesn't
		// corrupt the 504 we sent.
		w.mu.Unlock()
		return len(b), nil
	}
	w.claimed = true
	w.mu.Unlock()
	return w.ResponseWriter.Write(b)
}

func writeTimeoutError(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusGatewayTimeout)
	body, err := json.Marshal(base.ErrorResponse{
		Title:  "Request timed out",
		Code:   string(base.ErrorCodeRequestTimeout),
		Detail: "The server took too long to produce a response",
	})
	if err == nil {
		_, _ = writer.Write(body)
	}
}
