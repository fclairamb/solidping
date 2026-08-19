package models

import (
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
)

// ActorType represents who triggered an event.
type ActorType string

const (
	// ActorTypeSystem indicates the event was triggered by the system.
	ActorTypeSystem ActorType = "system"
	// ActorTypeUser indicates the event was triggered by a user.
	ActorTypeUser ActorType = "user"
)

// Event represents an audit log entry.
type Event struct {
	UID             string    `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string    `bun:"organization_uid,notnull"`
	IncidentUID     *string   `bun:"incident_uid"`
	CheckUID        *string   `bun:"check_uid"`
	JobUID          *string   `bun:"job_uid"`
	EventType       EventType `bun:"event_type,notnull"`
	ActorType       ActorType `bun:"actor_type,notnull"`
	ActorUID        *string   `bun:"actor_uid"`
	Payload         JSONMap   `bun:"payload,type:jsonb,nullzero"`
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp"`
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
	EventTypes      []EventType // Optional: filter by event types
	ActorType       *ActorType  // Optional: filter by actor type
	Since           *time.Time  // Optional: events created after this time
	Until           *time.Time  // Optional: events created before this time

	// Cursor-based pagination
	CursorTimestamp *time.Time // Optional: events with created_at < this timestamp
	CursorUID       *string    // Optional: for same timestamp, events with UID < this

	Limit int // Optional: pagination limit
}
