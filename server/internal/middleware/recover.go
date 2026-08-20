package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/getsentry/sentry-go"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// ErrPanic wraps a recovered panic so the middleware above the recoverer see a
// perfectly ordinary failed request.
var ErrPanic = errors.New("panic serving HTTP request")

// panicResponder writes the standard error body. HandlerBase needs no config
// for that path, and a middleware has no config to hand it anyway.
//
//nolint:gochecknoglobals // stateless writer for the one canned 500 body
var panicResponder = base.NewHandlerBase(nil)

// Recoverer catches panics from the handler chain, reports them to Sentry
// exactly once, and turns them into a normal 500 JSON response.
//
// Before this existed, nothing recovered: chi does not unless
// chi/middleware.Recoverer is mounted, httpx.Router delegates straight to the
// mux, and the old SentryMiddleware re-panicked into a "recovery middleware"
// that had been lost in the bunrouter->chi migration. The panic unwound to
// net/http, which logged `http: panic serving` and dropped the connection — the
// client saw a reset, not a 500.
//
// Placement matters and is pinned by tests:
//
//   - inside SentryMiddleware, so the request-scoped hub (and the user/org
//     attached by RequireAuth) is on the context and this is the ONLY component
//     capturing the panic — one panic, one event;
//   - inside the logging and HTTP-metrics middleware, so the request is logged
//     and counted as an ordinary 500 rather than vanishing from /metrics.
//
// http.ErrAbortHandler keeps its stdlib meaning: abort this response silently.
// It is re-panicked untouched and never reported.
func Recoverer(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) (err error) {
		// Wrapping lets us tell "the handler panicked before writing anything"
		// (write the canned 500) from "it panicked mid-response" (appending a
		// second body would corrupt the first).
		rec := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}

		defer func() {
			panicValue := recover()
			if panicValue == nil {
				return
			}

			if errors.Is(asError(panicValue), http.ErrAbortHandler) {
				panic(panicValue)
			}

			stack := debug.Stack()

			hub := sentry.GetHubFromContext(req.Context())
			if hub == nil {
				hub = sentry.CurrentHub()
			}

			hub.RecoverWithContext(req.Context(), panicValue)

			slog.ErrorContext(req.Context(), "panic serving HTTP request",
				"method", req.Method,
				"path", req.URL.Path,
				"panic", fmt.Sprint(panicValue),
				"stack", string(stack),
			)

			if !rec.wroteHeader {
				_ = panicResponder.WriteError(
					rec, http.StatusInternalServerError, base.ErrorCodeInternalError, "Internal server error")
			}

			err = fmt.Errorf("%w: %v", ErrPanic, panicValue)
		}()

		return next(rec, req)
	}
}

// asError returns the panic value as an error when it is one, so
// errors.Is(..., http.ErrAbortHandler) works for a wrapped abort too.
func asError(v any) error {
	if e, ok := v.(error); ok {
		return e
	}

	return nil
}
