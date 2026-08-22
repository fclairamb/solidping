// Package audit writes the org-scoped security and configuration audit trail
// (spec 2026-08-21-09) into the existing `events` table.
//
// Two rules shape this package:
//
//  1. **Emission happens in the service layer, never in HTTP middleware.**
//     An event written from middleware could only ever say "a PATCH hit this
//     URL"; an event written from the service that performed the change says
//     which policy was edited and which fields moved, and it also fires for
//     internal callers that never went through HTTP at all. Middleware's only
//     job here is to *capture* who is calling and from where (WithRequest /
//     WithUser below) and park it on the context so the service layer can
//     reach it without every service signature growing two more parameters.
//
//  2. **Nothing sensitive is ever stored.** Every payload goes through
//     Redact on the way in: no secrets, no password material, no token
//     values, no full config payloads. Update events carry changed FIELD
//     NAMES plus safe scalar old→new values, and nothing else.
//
// Recording is strictly best-effort: a failed audit write is logged and
// swallowed, never propagated, because losing the trail is bad but failing
// the user's actual operation because the trail could not be written is
// worse.
package audit

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// EventStore is the narrow slice of db.Service this package needs. Keeping it
// narrow means every emitting service can pass the `db` it already holds, and
// tests can substitute a slice-backed fake in three lines.
type EventStore interface {
	CreateEvent(ctx context.Context, event *models.Event) error
	UpdateEventPayload(ctx context.Context, uid string, payload models.JSONMap) error
}

// Payload keys that are part of the audit contract (dash0 and the docs both
// read them). Spelled once here so a typo cannot silently split a field in
// two across emitters.
const (
	// PayloadKeyTargetType names the kind of object acted on ("integration",
	// "escalation_policy", …).
	PayloadKeyTargetType = "target_type"
	// PayloadKeyTargetUID is the acted-on object's UID.
	PayloadKeyTargetUID = "target_uid"
	// PayloadKeyTargetName is its human-readable name at the time of the event.
	PayloadKeyTargetName = "target_name"
	// PayloadKeyChangedFields lists the field names an update touched.
	PayloadKeyChangedFields = "changed_fields"
	// PayloadKeyChanges holds safe scalar old→new values for the non-sensitive
	// subset of PayloadKeyChangedFields.
	PayloadKeyChanges = "changes"
)

// captureIP mirrors the `audit.capture_ip` config knob (default on). A
// GDPR-sensitive self-hoster turns it off and source_ip stays NULL on every
// row; the user agent is unaffected, being far less identifying.
//
// It is process-global because the alternative — threading an audit config
// through forty service constructors — buys nothing: there is exactly one
// setting, for the whole process, for the lifetime of the process.
var captureIP atomic.Bool

func init() { captureIP.Store(true) }

// SetCaptureIP applies the `audit.capture_ip` knob. Called once from app
// wiring; tests flip it directly and restore it with a defer.
func SetCaptureIP(enabled bool) { captureIP.Store(enabled) }

// CaptureIPEnabled reports the current setting.
func CaptureIPEnabled() bool { return captureIP.Load() }

// Actor is who caused an event and where the request came from.
type Actor struct {
	// Type is the events.actor_type value. Zero value means "unknown", and
	// Record normalizes it to ActorTypeSystem.
	Type models.ActorType
	// UserUID is the acting user — the column the spec calls actor_user_uid.
	// Empty for system- and service-originated events.
	UserUID string
	// SourceIP is the client address, already extracted from the trusted
	// forwarding headers by the caller. Dropped at write time when
	// audit.capture_ip is off.
	SourceIP string
	// UserAgent is the raw User-Agent header.
	UserAgent string
}

type actorContextKey struct{}

// WithRequest records the request provenance on the context. Called by the
// request-meta middleware for EVERY request, authenticated or not — the
// login attempt that gets rejected is precisely the one whose IP matters.
func WithRequest(ctx context.Context, sourceIP, userAgent string) context.Context {
	actor := ActorFromContext(ctx)
	// Normalized HERE, at capture, not only at write time. The raw value is
	// "host:port" with an ephemeral port that differs on every request, and
	// the failed-login folder keys on (org, email, IP) — an unnormalized
	// address would make every attempt look like it came from a new client and
	// silently defeat the folding entirely.
	actor.SourceIP = NormalizeSourceIP(sourceIP)
	actor.UserAgent = truncate(userAgent, maxUserAgentLen)

	return context.WithValue(ctx, actorContextKey{}, actor)
}

// WithUser records the authenticated principal on the context, preserving any
// request provenance already there. Called by RequireAuth once the token has
// been validated.
func WithUser(ctx context.Context, userUID string, actorType models.ActorType) context.Context {
	actor := ActorFromContext(ctx)
	actor.UserUID = userUID

	if actorType.IsValid() {
		actor.Type = actorType
	}

	return context.WithValue(ctx, actorContextKey{}, actor)
}

// WithActor replaces the whole actor. Mostly for tests and for the auth
// service, which knows who is logging in before any middleware could.
func WithActor(ctx context.Context, actor Actor) context.Context {
	actor.SourceIP = NormalizeSourceIP(actor.SourceIP)
	actor.UserAgent = truncate(actor.UserAgent, maxUserAgentLen)

	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorFromContext returns the actor recorded on the context, or the zero
// Actor (which Record treats as the system) when there is none.
func ActorFromContext(ctx context.Context) Actor {
	if actor, ok := ctx.Value(actorContextKey{}).(Actor); ok {
		return actor
	}

	return Actor{}
}

// Target identifies the object an event is about. Empty fields are omitted
// from the payload rather than written as "".
type Target struct {
	Type string
	UID  string
	Name string
}

const (
	maxUserAgentLen = 512
	// maxSourceIPLen matches the events.source_ip column (varchar(45) on
	// Postgres — the longest possible textual IPv6 address).
	maxSourceIPLen = 45
)

// NormalizeSourceIP reduces a raw remote address to the bare IP the audit
// trail stores.
//
// This is not cosmetic. http.Request.RemoteAddr is "host:port", and for IPv6
// that is "[2001:db8:85a3:8d3:1319:8a2e:370:7348]:65535" — 47 characters,
// which does not fit the varchar(45) column and would make the INSERT fail
// outright on Postgres while quietly succeeding on SQLite. The ephemeral
// source port is also worthless to a reader: it identifies a TCP connection
// that no longer exists, not a client.
//
// Values that are already bare (the X-Forwarded-For path, which carries no
// port) pass through untouched.
func NormalizeSourceIP(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "unknown" {
		return ""
	}

	if host, _, err := net.SplitHostPort(trimmed); err == nil && host != "" {
		trimmed = host
	}

	// A bracketed IPv6 literal with no port ("[::1]") is not host:port, so
	// SplitHostPort leaves it alone.
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")

	return truncate(trimmed, maxSourceIPLen)
}

// Record writes one audit event, best-effort.
//
// The actor comes from the context (see WithRequest / WithUser); the payload
// is redacted; source_ip is dropped when audit.capture_ip is off. Returns the
// event that was written so a caller that needs to fold later attempts (only
// auth.login_failed does) can hold on to its UID; nil when nothing was
// written.
func Record(
	ctx context.Context,
	store EventStore,
	orgUID string,
	eventType models.EventType,
	target Target,
	payload models.JSONMap,
) *models.Event {
	if store == nil || orgUID == "" {
		return nil
	}

	event := NewEvent(ctx, orgUID, eventType, target, payload)

	if err := store.CreateEvent(ctx, event); err != nil {
		// Deliberately swallowed: the business operation already succeeded and
		// must not be rolled back because the trail could not be appended to.
		slog.ErrorContext(ctx, "Failed to record audit event",
			"error", err, "eventType", string(eventType), "orgUid", orgUID)

		return nil
	}

	return event
}

// NewEvent builds the row Record would write, without writing it. Exported so
// the login-failed folder can construct and inspect one, and so tests can
// assert on redaction without a store.
func NewEvent(
	ctx context.Context,
	orgUID string,
	eventType models.EventType,
	target Target,
	payload models.JSONMap,
) *models.Event {
	actor := ActorFromContext(ctx)

	actorType := actor.Type
	if !actorType.IsValid() {
		if actor.UserUID != "" {
			actorType = models.ActorTypeUser
		} else {
			actorType = models.ActorTypeSystem
		}
	}

	event := models.NewEvent(orgUID, eventType, actorType)

	if actor.UserUID != "" {
		uid := actor.UserUID
		event.ActorUID = &uid
	}

	if captureIP.Load() {
		if ip := NormalizeSourceIP(actor.SourceIP); ip != "" {
			event.SourceIP = &ip
		}
	}

	if actor.UserAgent != "" {
		agent := truncate(actor.UserAgent, maxUserAgentLen)
		event.UserAgent = &agent
	}

	event.Payload = buildPayload(target, payload)

	return event
}

func buildPayload(target Target, payload models.JSONMap) models.JSONMap {
	out := Redact(payload)
	if out == nil {
		out = models.JSONMap{}
	}

	if target.Type != "" {
		out[PayloadKeyTargetType] = target.Type
	}

	if target.UID != "" {
		out[PayloadKeyTargetUID] = target.UID
	}

	if target.Name != "" {
		out[PayloadKeyTargetName] = truncate(target.Name, maxValueLen)
	}

	return out
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}

	return value[:limit]
}
