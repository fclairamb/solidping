// Package errorreport centralizes "which Sentry hub does this code path report
// on?" so handlers, middleware and background workers can never drift apart.
//
// Every report goes to two independent sinks: Sentry (a no-op without a DSN)
// and PostHog error tracking via analytics.CaptureException (a no-op unless
// PostHog is active). Neither depends on the other.
//
// Two rules, deliberately different for Sentry:
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
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/getsentry/sentry-go"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Headers the dashboard sends so a server exception can be linked to its
// PostHog session replay.
const (
	// HeaderSessionID carries posthog.get_session_id().
	HeaderSessionID = "X-Posthog-Session-Id"
	// HeaderDistinctID carries posthog.get_distinct_id().
	HeaderDistinctID = "X-Posthog-Distinct-Id"

	// serverDistinctID is the distinct id of a worker fault with no organization.
	serverDistinctID = "server"
)

var headerValuePattern = regexp.MustCompile(`^[A-Za-z0-9_:/.\-]{1,200}$`)

// ErrPanic wraps a recovered panic value that is not an error.
var ErrPanic = errors.New("panic")

type identityKey struct{}

// identity is the authenticated principal of a request, filled in after the
// context was created (authentication runs later than SentryMiddleware), hence
// a mutable holder rather than a context value.
type identity struct{ orgUID, userUID string }

// WithIdentityHolder installs an empty principal holder on ctx. Called once per
// request by SentryMiddleware.
func WithIdentityHolder(ctx context.Context) context.Context {
	return context.WithValue(ctx, identityKey{}, &identity{})
}

// SetIdentity records the authenticated principal on the holder installed by
// WithIdentityHolder. A no-op when there is none.
func SetIdentity(ctx context.Context, orgUID, userUID string) {
	if id, ok := ctx.Value(identityKey{}).(*identity); ok {
		id.orgUID, id.userUID = orgUID, userUID
	}
}

// CaptureOnRequest reports err for the request: on the request-scoped Sentry
// hub (skipped when absent, so non-HTTP callers stay silent there) and to
// PostHog error tracking.
func CaptureOnRequest(req *http.Request, err error) {
	if req == nil || err == nil {
		return
	}

	if hub := sentry.GetHubFromContext(req.Context()); hub != nil {
		hub.CaptureException(err)
	}

	analytics.CaptureException(req.Context(), requestException(req, err, ""))
}

// RecoverOnRequest reports a recovered panic. Sentry gets it on the request
// hub (falling back to the current hub), PostHog as an exception carrying the
// stack.
func RecoverOnRequest(req *http.Request, panicValue any, stack []byte) {
	hub := sentry.GetHubFromContext(req.Context())
	if hub == nil {
		hub = sentry.CurrentHub()
	}

	hub.RecoverWithContext(req.Context(), panicValue)

	err, ok := panicValue.(error)
	if !ok {
		err = fmt.Errorf("%w: %v", ErrPanic, panicValue)
	}

	analytics.CaptureException(req.Context(), requestException(req, err, string(stack)))
}

// requestException builds the PostHog event for an HTTP request. Only the
// method and the route pattern are attached: never the raw path, query string,
// headers or body.
func requestException(req *http.Request, err error, stack string) *analytics.Exception {
	exc := &analytics.Exception{
		Err:   err,
		Stack: stack,
		Tags:  map[string]string{"http.method": req.Method, "http.route": httpx.RoutePattern(req)},
	}

	if id, ok := req.Context().Value(identityKey{}).(*identity); ok && (id.userUID != "" || id.orgUID != "") {
		exc.OrgUID, exc.UserUID = id.orgUID, id.userUID
	} else if v := req.Header.Get(HeaderDistinctID); headerValuePattern.MatchString(v) {
		exc.DistinctID = v
	}

	if v := req.Header.Get(HeaderSessionID); headerValuePattern.MatchString(v) {
		exc.SessionID = v
	}

	return exc
}

// CaptureWithTags reports err with the given tags to both sinks, on the context
// hub when one is present and on the current (process-wide) hub otherwise. Tags
// with an empty value are skipped so an absent field never becomes a "" facet.
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

	exc := &analytics.Exception{Err: err, Tags: tags, DistinctID: serverDistinctID}
	if org := tags["organization"]; org != "" {
		exc.DistinctID, exc.OrgUID = "", org
	}

	analytics.CaptureException(ctx, exc)
}
