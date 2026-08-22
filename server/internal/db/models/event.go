package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// EventType represents the type of an audit event.
type EventType string

const (
	// EventTypeCheckCreated indicates a check was created.
	EventTypeCheckCreated EventType = "check.created"
	// EventTypeCheckUpdated indicates a check was updated.
	EventTypeCheckUpdated EventType = "check.updated"
	// EventTypeCheckDeleted indicates a check was deleted.
	EventTypeCheckDeleted EventType = "check.deleted"

	// EventTypeIncidentCreated indicates an incident was created.
	EventTypeIncidentCreated EventType = "incident.created"
	// EventTypeIncidentEscalated indicates an incident was escalated.
	EventTypeIncidentEscalated EventType = "incident.escalated"
	// EventTypeIncidentEscalationFailed indicates an escalation step
	// could not be delivered (empty schedule, missing user, etc.). Soft
	// failure — subsequent steps still fire.
	EventTypeIncidentEscalationFailed EventType = "incident.escalation_failed"
	// EventTypeIncidentResolved indicates an incident was resolved.
	EventTypeIncidentResolved EventType = "incident.resolved"
	// EventTypeIncidentReopened indicates an incident was reopened after a relapse.
	EventTypeIncidentReopened EventType = "incident.reopened"
	// EventTypeIncidentAcknowledged indicates an incident was acknowledged.
	EventTypeIncidentAcknowledged EventType = "incident.acknowledged"
	// EventTypeIncidentUnacknowledged indicates an acknowledgment was cleared.
	EventTypeIncidentUnacknowledged EventType = "incident.unacknowledged"
	// EventTypeIncidentSnoozed indicates an incident was snoozed until a future time.
	EventTypeIncidentSnoozed EventType = "incident.snoozed"
	// EventTypeIncidentUnsnoozed indicates an incident's snooze was cleared.
	EventTypeIncidentUnsnoozed EventType = "incident.unsnoozed"
	// EventTypeIncidentComment is a free-text, user-authored comment on an
	// incident, ingested from the dashboard or a Slack thread reply. The
	// payload carries `text`, `source` (web|slack) and, for Slack-authored
	// comments, the Slack author attribution (slackUserId, slackUserName,
	// slackTeamId, slackTs). Append-only, like every other event row.
	EventTypeIncidentComment EventType = "incident.comment"

	// EventTypeStatusPageIncidentPublished indicates an incident became visible
	// on a status page (spec 2026-08-19-08). It is deliberately DISTINCT from
	// EventTypeIncidentCreated: an operational incident opening and a
	// customer-visible incident being published are different facts, they
	// happen at different times (the auto-publish debounce sits between them),
	// and a great many incidents never produce the second one at all. A
	// webhook consumer must be able to subscribe to one without the other.
	EventTypeStatusPageIncidentPublished EventType = "statuspage.incident.published"
	// EventTypeStatusPageIncidentUpdated indicates a publication's public
	// title, severity, state or narrative changed.
	EventTypeStatusPageIncidentUpdated EventType = "statuspage.incident.updated"
	// EventTypeStatusPageIncidentResolved indicates a publication was closed
	// (or unpublished). The internal incident.resolved event is unchanged and
	// still fires on its own schedule.
	EventTypeStatusPageIncidentResolved EventType = "statuspage.incident.resolved"

	// EventTypeStatusSubscriberDisabled indicates a webhook/Slack status-page
	// subscription was disabled after repeated delivery failures (spec
	// 2026-08-21-07). Without it the only symptom of a broken webhook is
	// "we stopped getting notifications" — which nobody notices until an
	// incident.
	EventTypeStatusSubscriberDisabled EventType = "statuspage.subscriber.disabled"

	// EventTypeStatusUpdateCreated indicates a status update was created.
	EventTypeStatusUpdateCreated EventType = "status_update.created"
	// EventTypeStatusUpdateUpdated indicates a status update was modified.
	EventTypeStatusUpdateUpdated EventType = "status_update.updated"
	// EventTypeStatusUpdateDeleted indicates a status update was soft-deleted.
	EventTypeStatusUpdateDeleted EventType = "status_update.deleted"

	// EventTypeOrgActivationSignupCompleted fires once per org when its
	// initial member is created (the user who provisioned the org).
	EventTypeOrgActivationSignupCompleted EventType = "org.activation.signup_completed"
	// EventTypeOrgActivationFirstCheckCreated fires once per org the first
	// time a check is created for it.
	EventTypeOrgActivationFirstCheckCreated EventType = "org.activation.first_check_created"
	// EventTypeOrgActivationFirstResultReceived fires once per org the first
	// time a check result is recorded for it.
	EventTypeOrgActivationFirstResultReceived EventType = "org.activation.first_result_received"
	// EventTypeOrgActivationFirstNotificationConfigured fires once per org
	// the first time an integration connection is created for it.
	EventTypeOrgActivationFirstNotificationConfigured EventType = "org.activation.first_notification_configured"
	// EventTypeOrgActivationFirstIncidentPaged fires once per org the first
	// time an incident notification is dispatched for it.
	EventTypeOrgActivationFirstIncidentPaged EventType = "org.activation.first_incident_paged"

	// --- Security / configuration audit trail (spec 2026-08-21-09) -------------
	//
	// Everything below is written by internal/audit at the SERVICE layer, never
	// by HTTP middleware, so an event carries domain meaning and internal
	// callers are covered too. Payloads are redacted on the way in
	// (audit.Redact): no secrets, no password material, no token values, no
	// full config payloads — changed FIELD NAMES, safe scalar old→new values,
	// and target identity only.

	// EventTypeAuthLoginSucceeded records a successful authentication. The
	// payload carries `method` (password / ldap / passkey / oauth / oidc /
	// saml), never any credential.
	EventTypeAuthLoginSucceeded EventType = "auth.login_succeeded"
	// EventTypeAuthLoginFailed records a rejected authentication attempt. It is
	// a brute-force amplification vector, so it is the one event type that is
	// NOT written one-row-per-occurrence: repeats of the same (org, email, IP)
	// inside a short window fold into a single row with a `count`, and a
	// per-org hourly ceiling caps how many rows can be created at all. See
	// internal/audit/loginfailed.go.
	EventTypeAuthLoginFailed EventType = "auth.login_failed"
	// EventTypeAuthLogout records a session being ended deliberately.
	EventTypeAuthLogout EventType = "auth.logout"
	// EventTypeAuthTokenCreated records an API token or agent enrollment key
	// being minted. The payload carries the token's NAME and PREFIX only —
	// never the value, which the server itself only ever sees hashed.
	EventTypeAuthTokenCreated EventType = "auth.token_created"
	// EventTypeAuthTokenRevoked records an API token or agent key being revoked.
	EventTypeAuthTokenRevoked EventType = "auth.token_revoked"

	// EventTypeMemberInvited records an invitation being sent.
	EventTypeMemberInvited EventType = "member.invited"
	// EventTypeMemberJoined records a membership row being created — whether by
	// an accepted invitation or by an admin adding someone directly.
	EventTypeMemberJoined EventType = "member.joined"
	// EventTypeMemberRemoved records a membership being revoked.
	EventTypeMemberRemoved EventType = "member.removed"
	// EventTypeMemberRoleChanged records a role moving. Emitted only when the
	// role actually changed, so a no-op PATCH does not manufacture an event.
	EventTypeMemberRoleChanged EventType = "member.role_changed"

	// EventTypeIntegrationCreated records a notification integration being added.
	EventTypeIntegrationCreated EventType = "integration.created"
	// EventTypeIntegrationUpdated records an integration being edited. The
	// payload lists changed field names; credential-bearing fields are named
	// but never valued.
	EventTypeIntegrationUpdated EventType = "integration.updated"
	// EventTypeIntegrationDeleted records an integration being removed.
	EventTypeIntegrationDeleted EventType = "integration.deleted"

	// EventTypeEscalationPolicyCreated records an escalation policy being added.
	EventTypeEscalationPolicyCreated EventType = "escalation_policy.created"
	// EventTypeEscalationPolicyUpdated records an escalation policy being edited.
	EventTypeEscalationPolicyUpdated EventType = "escalation_policy.updated"
	// EventTypeEscalationPolicyDeleted records an escalation policy being removed.
	EventTypeEscalationPolicyDeleted EventType = "escalation_policy.deleted"

	// EventTypeOnCallScheduleCreated records an on-call schedule being added.
	EventTypeOnCallScheduleCreated EventType = "oncall_schedule.created"
	// EventTypeOnCallScheduleUpdated records an on-call schedule being edited.
	EventTypeOnCallScheduleUpdated EventType = "oncall_schedule.updated"
	// EventTypeOnCallScheduleDeleted records an on-call schedule being removed.
	EventTypeOnCallScheduleDeleted EventType = "oncall_schedule.deleted"

	// EventTypeStatusPageCreated records a status page being added. Distinct
	// from the statuspage.incident.* family, which is about what a page
	// PUBLISHES rather than about the page's own configuration.
	EventTypeStatusPageCreated EventType = "status_page.created"
	// EventTypeStatusPageUpdated records a status page's configuration changing.
	EventTypeStatusPageUpdated EventType = "status_page.updated"
	// EventTypeStatusPageDeleted records a status page being removed.
	EventTypeStatusPageDeleted EventType = "status_page.deleted"

	// EventTypeMaintenanceWindowCreated records a maintenance window being added.
	EventTypeMaintenanceWindowCreated EventType = "maintenance_window.created"
	// EventTypeMaintenanceWindowUpdated records a maintenance window being edited.
	EventTypeMaintenanceWindowUpdated EventType = "maintenance_window.updated"
	// EventTypeMaintenanceWindowDeleted records a maintenance window being removed.
	EventTypeMaintenanceWindowDeleted EventType = "maintenance_window.deleted"

	// EventTypeConfigApplied records a config-as-code apply. The payload holds
	// the SUMMARY COUNTS (created / updated / deleted / unmanaged) and the
	// manifest name — deliberately never the manifest body, which routinely
	// carries secret references.
	EventTypeConfigApplied EventType = "config.applied"
	// EventTypeOrgSettingsUpdated records an organization-level setting change,
	// as a list of changed field names plus safe scalar values.
	EventTypeOrgSettingsUpdated EventType = "org.settings_updated"
)

// ActorType represents who triggered an event.
type ActorType string

const (
	// ActorTypeSystem indicates the event was triggered by the system.
	ActorTypeSystem ActorType = "system"
	// ActorTypeUser indicates the event was triggered by a user.
	ActorTypeUser ActorType = "user"
	// ActorTypeAPIToken indicates the event was triggered through a personal
	// access token or agent key rather than an interactive session. ActorUID
	// still names the owning user when one is known.
	ActorTypeAPIToken ActorType = "api_token"
	// ActorTypeService indicates the event was triggered by another trusted
	// service (a signed service-to-service call, e.g. the billing service).
	ActorTypeService ActorType = "service"
)

// IsValid reports whether the actor type is one the schema accepts. The
// events.actor_type check constraint enumerates exactly these four values, so
// an unknown value must never reach an INSERT.
func (a ActorType) IsValid() bool {
	switch a {
	case ActorTypeSystem, ActorTypeUser, ActorTypeAPIToken, ActorTypeService:
		return true
	default:
		return false
	}
}

// Event represents an audit log entry.
type Event struct {
	UID             string    `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string    `bun:"organization_uid,notnull"`
	IncidentUID     *string   `bun:"incident_uid"`
	CheckUID        *string   `bun:"check_uid"`
	JobUID          *string   `bun:"job_uid"`
	EventType       EventType `bun:"event_type,notnull"`
	ActorType       ActorType `bun:"actor_type,notnull"`
	// ActorUID is the acting user's UID — this column IS the spec's
	// `actor_user_uid` (2026-08-21-09). It predates that spec as an FK to
	// users(uid); a second column of the same meaning would only create a
	// split brain, so the API exposes it under the `actorUserUid` parameter
	// name while the column keeps its original name.
	ActorUID *string `bun:"actor_uid"`
	Payload  JSONMap `bun:"payload,type:jsonb,nullzero"`
	// SourceIP is the client address the action came from, when the request
	// had one and audit.capture_ip is on. Nil for system-originated events and
	// for deployments that turned IP capture off for GDPR reasons.
	SourceIP *string `bun:"source_ip"`
	// UserAgent is the raw User-Agent header, truncated. Nil when absent.
	UserAgent *string   `bun:"user_agent"`
	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// NewEvent creates a new event with generated UID.
func NewEvent(orgUID string, eventType EventType, actorType ActorType) *Event {
	return &Event{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		EventType:       eventType,
		ActorType:       actorType,
		Payload:         make(JSONMap),
		CreatedAt:       time.Now(),
	}
}

// ListEventsFilter provides filtering options for listing events.
type ListEventsFilter struct {
	OrganizationUID string      // Required: organization scope
	IncidentUID     *string     // Optional: filter by incident UID
	CheckUID        *string     // Optional: filter by check UID
	EventTypes      []EventType // Optional: filter by exact event types
	// EventTypePrefixes filters by event-type FAMILY: "auth" matches every
	// auth.* type. Combined with EventTypes as an OR (either predicate may
	// admit a row); empty means "no family restriction".
	EventTypePrefixes []string
	// ExcludeEventTypePrefixes hides whole families. This is the server-side
	// half of "auth events are admin-only": a non-admin caller always carries
	// ExcludeEventTypePrefixes=["auth"], so neither an unfiltered listing nor
	// an explicit ?type=auth can leak them.
	ExcludeEventTypePrefixes []string
	ActorType                *ActorType // Optional: filter by actor type
	// ActorUID filters to the events one user caused (API: actorUserUid).
	ActorUID *string
	Since    *time.Time // Optional: events created after this time
	Until    *time.Time // Optional: events created before this time

	// Cursor-based pagination
	CursorTimestamp *time.Time // Optional: events with created_at < this timestamp
	CursorUID       *string    // Optional: for same timestamp, events with UID < this

	Limit int // Optional: pagination limit
}

// EventTypeLikeEscape is the escape character the event-type family predicates
// pair with LIKE. It must be spelled the same way in every dialect's query.
const EventTypeLikeEscape = `\`

// EventTypeLikePattern turns a family prefix ("auth", or "auth." — both are
// accepted) into the SQL LIKE pattern matching every type in that family.
//
// The escaping is not paranoia: real family names contain `_`, which LIKE
// treats as "any single character". Without it, a filter on
// "oncall_schedule" would also admit a hypothetical "oncallXschedule.*"
// family, and the *exclusion* used to hide auth events from non-admins would
// be the dangerous direction of the same bug.
func EventTypeLikePattern(prefix string) string {
	trimmed := strings.TrimSuffix(prefix, ".")
	escaped := strings.NewReplacer(`\`, `\\`, "_", `\_`, "%", `\%`).Replace(trimmed)

	return escaped + ".%"
}
