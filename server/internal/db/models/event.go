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
	// EventTypeIncidentResolved indicates an incident was resolved.
	EventTypeIncidentResolved EventType = "incident.resolved"
	// EventTypeIncidentReopened indicates an incident was reopened after a relapse.
	EventTypeIncidentReopened EventType = "incident.reopened"
	// EventTypeIncidentAcknowledged indicates an incident was acknowledged.
	EventTypeIncidentAcknowledged EventType = "incident.acknowledged"
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
