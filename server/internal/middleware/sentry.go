// Package middleware provides HTTP middleware for authentication, authorization, and error tracking.
package middleware

import (
	"net/http"

	"github.com/getsentry/sentry-go"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// SentryMiddleware returns a bunrouter middleware that integrates Sentry error tracking.
// It clones the Sentry hub per request, sets request context and user info,
// and recovers panics to report them to Sentry before re-panicking.
func SentryMiddleware() bunrouter.MiddlewareFunc {
	return func(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
		return func(w http.ResponseWriter, req bunrouter.Request) error {
			hub := sentry.GetHubFromContext(req.Context())
			if hub == nil {
				hub = sentry.CurrentHub().Clone()
			}

			hub.Scope().SetRequest(req.Request)

			// Add user context if authenticated
			if claims, ok := req.Context().Value(base.ContextKeyClaims).(*auth.Claims); ok && claims != nil {
				hub.Scope().SetUser(sentry.User{
					ID: claims.UserUID,
				})
				hub.Scope().SetTag("organization", claims.OrgSlug)
			}

			ctx := sentry.SetHubOnContext(req.Context(), hub)
			req = req.WithContext(ctx)

			defer func() {
				if r := recover(); r != nil {
					hub.RecoverWithContext(ctx, r)
					// Re-panic so the existing recovery middleware can handle the HTTP response
					panic(r)
				}
			}()

			return next(w, req)
		}
	}
}
