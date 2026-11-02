// Package analytics is SolidPing's product-analytics (PostHog) client.
//
// # Off unless configured
//
// The package is designed so that a deployment which has NOT configured
// PostHog does literally nothing: New returns a genuine no-op implementation
// that constructs no HTTP client, opens no connection and allocates no buffer,
// and the package-level default is that no-op. Nothing in this package reaches
// the network unless config.PostHogConfig.Active() is true, i.e.
// `posthog.enabled == true && posthog.project_api_key != ""`.
//
// # Call sites never branch
//
// Following the Sentry wiring already used by the server, the client is a
// process-wide default installed once at startup (SetDefault) and used through
// the package-level Capture helper. Call sites therefore never ask "is
// analytics on" — they just Capture, and the no-op swallows it.
//
// # Error tracking
//
// Besides product events, the client forwards server faults (5xx responses,
// recovered panics) to PostHog error tracking as `$exception` events, through
// CaptureException. This is independent of Sentry: either sink works alone.
// The message and stack are redacted with the same rules as Sentry (see the
// scrub package); no request body, header, or query string is ever attached.
//
// # Privacy
//
// Events are keyed by a pseudonymous distinct id built from the organization
// UID and user UID only (see DistinctID). This package must never be handed an
// email address, a check target host/URL, an incident payload, or any
// customer-supplied free text; the exported Event type deliberately carries no
// field for them and the fixed event-name set below is small on purpose.
package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/posthog/posthog-go"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/scrub"
)

// Event names. Deliberately a small, closed set: it is far easier to add an
// event later than to purge one from a warehouse.
const (
	// EventOrgCreated fires when a new organization is created.
	EventOrgCreated = "org_created"
	// EventUserSignedUp fires when a user account is registered.
	EventUserSignedUp = "user_signed_up"
	// EventCheckCreated fires when a monitoring check is created. The check's
	// target is NEVER included — only its type.
	EventCheckCreated = "check_created"
	// EventIntegrationConnected fires when a notification integration
	// connection is created.
	EventIntegrationConnected = "integration_connected"
	// EventStatusPagePublished fires when a status page is published.
	EventStatusPagePublished = "status_page_published"
)

// Event is a product event. It intentionally offers no free-text field: the
// only caller-supplied payload is Properties, which must contain low-cardinality
// non-identifying values (a check type, a provider name), never a hostname, a
// URL, an email or user-entered text.
type Event struct {
	// Name is one of the Event* constants above.
	Name string
	// OrgUID is the organization UUID (pseudonymous).
	OrgUID string
	// UserUID is the acting user's UUID (pseudonymous). May be empty for
	// system-initiated actions.
	UserUID string
	// Properties are additional non-identifying event properties.
	Properties map[string]any
}

// Exception is a server fault reported to PostHog error tracking.
type Exception struct {
	// Err is the error to report. Required.
	Err error
	// Stack is an optional stack trace, redacted before sending.
	Stack string
	// OrgUID and UserUID build the distinct id (see DistinctID).
	OrgUID  string
	UserUID string
	// DistinctID, when non-empty, overrides the id built from OrgUID/UserUID.
	DistinctID string
	// SessionID is the PostHog session id the dashboard sent, if any; it links
	// the exception to the session replay.
	SessionID string
	// Tags are low-cardinality facets; empty values are skipped.
	Tags map[string]string
}

// Client captures product events.
type Client interface {
	// Capture records an event. It must never block the caller and never
	// return an error to a request path — delivery is best-effort.
	Capture(ctx context.Context, event Event)
	// CaptureException records a server fault as a `$exception` event. Same
	// contract as Capture: non-blocking, best-effort, never an error.
	CaptureException(ctx context.Context, exc *Exception)
	// Enabled reports whether this client actually sends anything.
	Enabled() bool
	// Close flushes buffered events. Safe to call on a no-op client.
	Close(ctx context.Context) error
}

// DistinctID builds the pseudonymous distinct id used for every event. The
// dashboard's src/lib/analytics.ts MUST build the same string, so a browser
// session and its server-side events stitch together in PostHog.
//
// Deliberately UUID-only: no email, no slug, no name.
func DistinctID(orgUID, userUID string) string {
	switch {
	case orgUID == "" && userUID == "":
		return "anonymous"
	case userUID == "":
		return "org:" + orgUID
	case orgUID == "":
		return "user:" + userUID
	default:
		return "org:" + orgUID + "/user:" + userUID
	}
}

// noopClient is a real no-op: it holds no state, starts no goroutine and
// touches no socket. This is what every unconfigured deployment gets.
type noopClient struct{}

func (noopClient) Capture(context.Context, Event)               {}
func (noopClient) CaptureException(context.Context, *Exception) {}
func (noopClient) Enabled() bool                                { return false }
func (noopClient) Close(context.Context) error                  { return nil }

// NewNoop returns the no-op client. Exported for tests and for call sites that
// want to be explicit about disabling analytics.
func NewNoop() Client { return noopClient{} }

// posthogClient is the real implementation, wrapping the async/buffered
// posthog-go client.
type posthogClient struct {
	client posthog.Client
}

// New builds the analytics client for the given configuration.
//
// It returns the no-op client — and constructs NOTHING else — whenever PostHog
// is not active. This is the single point where the "off unless configured"
// guarantee is enforced server-side; it applies exactly the same rule as
// GET /api/v1/config and the dashboard.
func New(cfg config.PostHogConfig) Client {
	if !cfg.Active() {
		return noopClient{}
	}

	phCfg := posthog.Config{
		Endpoint: cfg.ResolvedHost(),
		// Buffered/async delivery: Enqueue returns immediately and a
		// background goroutine ships batches, so no request path ever waits
		// on PostHog.
		Interval:        10 * time.Second,
		BatchSize:       50,
		ShutdownTimeout: 5 * time.Second,
	}

	// The personal API key is optional and only used for the server-side
	// privileged APIs. When absent the project key alone is used for capture.
	if cfg.PersonalAPIKey != "" {
		phCfg.SecretKey = cfg.PersonalAPIKey
	}

	client, err := posthog.NewWithConfig(cfg.ProjectAPIKey, phCfg)
	if err != nil {
		// A misconfigured analytics client must never prevent the server from
		// serving traffic — degrade to the no-op.
		//

		slog.Warn("analytics: failed to initialize PostHog client, product analytics disabled", "error", err)

		return noopClient{}
	}

	return &posthogClient{client: client}
}

// Capture enqueues an event. Never blocks, never returns an error to the
// caller: analytics must not be able to fail a user request.
func (c *posthogClient) Capture(ctx context.Context, event Event) {
	if event.Name == "" {
		return
	}

	props := posthog.NewProperties()
	for k, v := range event.Properties {
		props.Set(k, v)
	}

	// The organization UID is a pseudonymous identifier, safe to attach as a
	// property so events can be grouped per install/tenant.
	if event.OrgUID != "" {
		props.Set("orgUid", event.OrgUID)
	}

	if err := c.client.Enqueue(posthog.Capture{
		DistinctId: DistinctID(event.OrgUID, event.UserUID),
		Event:      event.Name,
		Properties: props,
	}); err != nil {
		slog.DebugContext(ctx, "analytics: failed to enqueue event", "event", event.Name, "error", err)
	}
}

// CaptureException enqueues a `$exception` event. Never blocks, never returns
// an error to the caller.
func (c *posthogClient) CaptureException(ctx context.Context, exc *Exception) {
	if exc == nil || exc.Err == nil {
		return
	}

	distinctID := exc.DistinctID
	if distinctID == "" {
		distinctID = DistinctID(exc.OrgUID, exc.UserUID)
	}

	props := posthog.NewProperties()
	for k, v := range exc.Tags {
		if v != "" {
			props.Set(k, v)
		}
	}

	if exc.OrgUID != "" {
		props.Set("orgUid", exc.OrgUID)
	}

	if exc.SessionID != "" {
		props.Set("$session_id", exc.SessionID)
	}

	if exc.Stack != "" {
		props.Set("stack", scrub.Text(exc.Stack))
	}

	handled := true

	if err := c.client.Enqueue(posthog.Exception{
		DistinctId: distinctID,
		Properties: props,
		ExceptionList: []posthog.ExceptionItem{{
			Type:      fmt.Sprintf("%T", exc.Err),
			Value:     scrub.Text(exc.Err.Error()),
			Mechanism: &posthog.ExceptionMechanism{Handled: &handled},
		}},
	}); err != nil {
		slog.DebugContext(ctx, "analytics: failed to enqueue exception", "error", err)
	}
}

// Enabled reports true — a posthogClient only exists when PostHog is active.
func (c *posthogClient) Enabled() bool { return true }

// Close flushes buffered events on graceful shutdown.
func (c *posthogClient) Close(_ context.Context) error {
	return c.client.Close()
}

// defaultClient is the process-wide client, mirroring how Sentry is wired.
// It starts as the no-op, so any code path that captures before (or without)
// analytics initialization is inert rather than nil-panicking.
var defaultClient atomic.Pointer[Client] //nolint:gochecknoglobals // process-wide like sentry

// SetDefault installs the process-wide analytics client. Passing nil resets to
// the no-op.
func SetDefault(c Client) {
	if c == nil {
		c = noopClient{}
	}

	defaultClient.Store(&c)
}

// Default returns the process-wide analytics client, never nil.
func Default() Client {
	if c := defaultClient.Load(); c != nil {
		return *c
	}

	return noopClient{}
}

// Capture records an event on the process-wide client. This is what call sites
// use; when analytics is not configured it is a no-op function call.
func Capture(ctx context.Context, event Event) {
	Default().Capture(ctx, event)
}

// CaptureException reports a server fault on the process-wide client; a no-op
// when PostHog is not configured.
func CaptureException(ctx context.Context, exc *Exception) {
	Default().CaptureException(ctx, exc)
}

// Close flushes the process-wide client. Called on graceful shutdown.
func Close(ctx context.Context) error {
	return Default().Close(ctx)
}
