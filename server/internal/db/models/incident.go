package models

import (
	"time"

	"github.com/google/uuid"
)

// IncidentState represents the state of an incident.
type IncidentState int

const (
	// IncidentStateActive indicates the incident is ongoing.
	IncidentStateActive IncidentState = 1
	// IncidentStateResolved indicates the incident has been resolved.
	IncidentStateResolved IncidentState = 2
)

// Incident represents a period when a check was down.
type Incident struct {
	UID             string        `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string        `bun:"organization_uid,notnull"`
	CheckUID        string        `bun:"check_uid,notnull"`
	Region          *string       `bun:"region"`
	State           IncidentState `bun:"state,notnull,default:1"`
	StartedAt       time.Time     `bun:"started_at,notnull"`
	ResolvedAt      *time.Time    `bun:"resolved_at"`
	EscalatedAt     *time.Time    `bun:"escalated_at"`
	AcknowledgedAt  *time.Time    `bun:"acknowledged_at"`
	AcknowledgedBy  *string       `bun:"acknowledged_by"`
	FailureCount    int           `bun:"failure_count,notnull,default:1"`
	RelapseCount    int           `bun:"relapse_count,notnull,default:0"`
	LastReopenedAt  *time.Time    `bun:"last_reopened_at"`
	Title           *string       `bun:"title"`
	Description     *string       `bun:"description"`
	Details         JSONMap       `bun:"details,type:jsonb,nullzero"`
	// CheckGroupUID is set on group incidents — NULL keeps the existing per-check semantics.
	CheckGroupUID *string    `bun:"check_group_uid"`
	CreatedAt     time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt     time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt     *time.Time `bun:"deleted_at"`
}

// NewIncident creates a new incident with generated UID.
func NewIncident(orgUID, checkUID string, startedAt time.Time, title string) *Incident {
	now := time.Now()

	return &Incident{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		State:           IncidentStateActive,
		StartedAt:       startedAt,
		FailureCount:    1,
		Title:           &title,
		Details:         make(JSONMap),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// IncidentUpdate represents fields that can be updated.
type IncidentUpdate struct {
	Region         *string
	State          *IncidentState
	ResolvedAt     *time.Time
	EscalatedAt    *time.Time
	AcknowledgedAt *time.Time
	AcknowledgedBy *string
	FailureCount   *int
	RelapseCount   *int
	LastReopenedAt *time.Time
	Title          *string
	Description    *string
	Details        *JSONMap

	// Clear* fields set columns to NULL on reopen
	ClearResolvedAt     bool
	ClearAcknowledgedAt bool
	ClearAcknowledgedBy bool
}

// IncidentMemberCheck tracks a single check's state inside a group incident.
type IncidentMemberCheck struct {
	IncidentUID      string     `bun:"incident_uid,pk"`
	CheckUID         string     `bun:"check_uid,pk"`
	JoinedAt         time.Time  `bun:"joined_at,notnull,default:current_timestamp"`
	FirstFailureAt   time.Time  `bun:"first_failure_at,notnull"`
	LastFailureAt    time.Time  `bun:"last_failure_at,notnull"`
	LastRecoveryAt   *time.Time `bun:"last_recovery_at"`
	FailureCount     int        `bun:"failure_count,notnull,default:1"`
	CurrentlyFailing bool       `bun:"currently_failing,notnull,default:true"`
}

// IncidentMemberUpdate represents fields that can be updated on a member row.
type IncidentMemberUpdate struct {
	LastFailureAt    *time.Time
	LastRecoveryAt   *time.Time
	FailureCount     *int
	CurrentlyFailing *bool
}

// ListIncidentsFilter provides filtering options for listing incidents.
type ListIncidentsFilter struct {
	OrganizationUID string          // Required: organization scope
	CheckUIDs       []string        // Optional: filter by check UIDs
	States          []IncidentState // Optional: filter by states (active, resolved)
	Since           *time.Time      // Optional: incidents started after this time
	Until           *time.Time      // Optional: incidents started before this time

	// Cursor-based pagination
	CursorTimestamp *time.Time // Optional: incidents with started_at < this timestamp
	CursorUID       *string    // Optional: for same timestamp, incidents with UID < this

	Limit int // Optional: pagination limit
}
