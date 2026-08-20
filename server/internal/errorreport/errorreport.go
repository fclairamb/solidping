// Package errorreport centralizes "which Sentry hub does this code path report
// on?" so handlers, middleware and background workers can never drift apart.
//
// Two rules, deliberately different:
//
//   - HTTP request paths report on the request-scoped hub only. SentryMiddleware
//     puts one on every request context, so a missing hub means "not an HTTP
//     request" (a unit test, a non-HTTP caller) and reporting is a no-op rather
//     than a surprise event on the process-wide hub.
//   - Background workers have no middleware, so they fall back to
//     sentry.CurrentHub() — which is the hub sentry.Init configured, or an
//     unbound no-op hub when no DSN was set.
package errorreport

import (
	"context"
	"net/http"

	"github.com/getsentry/sentry-go"
)

// CaptureOnRequest reports err on the request-scoped Sentry hub. It is a no-op
// when the request or the hub is absent, so non-HTTP callers stay silent.
func CaptureOnRequest(r *http.Request, err error) {
	if r == nil || err == nil {
		return
	}

	hub := sentry.GetHubFromContext(r.Context())
	if hub == nil {
		return
	}

	hub.CaptureException(err)
}

// CaptureWithTags reports err with the given tags, on the context hub when one
// is present and on the current (process-wide) hub otherwise. Tags with an
// empty value are skipped so an absent field never becomes a "" facet in the
// Sentry UI.
func CaptureWithTags(ctx context.Context, err error, tags map[string]string) {
	if err == nil {
		return
	}

	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentry.CurrentHub()
	}

	hub.WithScope(func(scope *sentry.Scope) {
		for key, value := range tags {
			if value == "" {
				continue
			}

			scope.SetTag(key, value)
		}

		hub.CaptureException(err)
	})
}
