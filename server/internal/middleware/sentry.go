// Package middleware provides HTTP middleware for authentication, authorization, and error tracking.
package middleware

import (
	"net/http"

	"github.com/getsentry/sentry-go"

	"github.com/fclairamb/solidping/server/internal/httpx"
)

// SentryMiddleware returns a middleware that puts a per-request Sentry hub on
// the request context: it clones the hub, attaches the request to its scope,
// and hands the context down.
//
// That is all it does, deliberately:
//
//   - It does NOT read the JWT claims. It runs before RequireAuth, so the
//     claims are never on the context yet — the block that used to live here
//     was dead code, and every event Sentry received was anonymous. Identity is
//     attached by RequireAuth / RequireMCPAuth instead (setSentryIdentity),
//     onto the very hub installed here.
//   - It does NOT recover panics. Recoverer does, and it is the single
//     reporter, so a panic can never produce two events.
func SentryMiddleware() httpx.Middleware {
	return func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) error {
			hub := sentry.GetHubFromContext(req.Context())
			if hub == nil {
				hub = sentry.CurrentHub().Clone()
			}

			hub.Scope().SetRequest(req)

			ctx := sentry.SetHubOnContext(req.Context(), hub)

			return next(writer, req.WithContext(ctx))
		}
	}
}
