// Package models provides data models for the SolidPing database.
package models

import (
	"time"

	"github.com/google/uuid"
)

// StatusUpdateKind represents the kind of a status update.
type StatusUpdateKind string

const (
	// StatusUpdateKindInvestigating indicates the team is investigating.
	StatusUpdateKindInvestigating StatusUpdateKind = "investigating"
	// StatusUpdateKindIdentified indicates the issue has been identified.
	StatusUpdateKindIdentified StatusUpdateKind = "identified"
	// StatusUpdateKindMonitoring indicates the fix is being monitored.
	StatusUpdateKindMonitoring StatusUpdateKind = "monitoring"
	// StatusUpdateKindResolved indicates the issue is resolved.
	StatusUpdateKindResolved StatusUpdateKind = "resolved"
	// StatusUpdateKindMaintenance indicates a scheduled maintenance.
	StatusUpdateKindMaintenance StatusUpdateKind = "maintenance"
	// StatusUpdateKindInfo is a general informational update.
	StatusUpdateKindInfo StatusUpdateKind = "info"
)

//nolint:gochecknoglobals // package-level lookup table for valid kinds.
var validStatusUpdateKinds = map[StatusUpdateKind]struct{}{
	StatusUpdateKindInvestigating: {},
	StatusUpdateKindIdentified:    {},
	StatusUpdateKindMonitoring:    {},
	StatusUpdateKindResolved:      {},
	StatusUpdateKindMaintenance:   {},
	StatusUpdateKindInfo:          {},
}

// IsValid returns true if the kind is one of the recognised values.
func (k StatusUpdateKind) IsValid() bool {
	_, ok := validStatusUpdateKinds[k]
	return ok
}

// StatusUpdate is an operator-written narrative post anchored to a status page.
type StatusUpdate struct {
	UID             string           `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string           `bun:"organization_uid,notnull"`
	StatusPageUID   string           `bun:"status_page_uid,notnull"`
	SectionUID      *string          `bun:"section_uid"`
	CheckUID        *string          `bun:"check_uid"`
	IncidentUID     *string          `bun:"incident_uid"`
	Title           string           `bun:"title,notnull"`
	BodyMarkdown    string           `bun:"body_markdown,notnull"`
	LinkURL         *string          `bun:"link_url"`
	Kind            StatusUpdateKind `bun:"kind,notnull"`
	PublishedAt     time.Time        `bun:"published_at,notnull,default:current_timestamp"`
	AuthorUID       string           `bun:"author_uid,notnull"`
	CreatedAt       time.Time        `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time        `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time       `bun:"deleted_at"`
}

// NewStatusUpdate creates a new StatusUpdate with generated UID and timestamps.
func NewStatusUpdate(orgUID, statusPageUID, authorUID string) *StatusUpdate {
	now := time.Now()

	return &StatusUpdate{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		StatusPageUID:   statusPageUID,
		AuthorUID:       authorUID,
		PublishedAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// StatusUpdatesFilter configures what to return from ListStatusUpdates.
type StatusUpdatesFilter struct {
	StatusPageUID string
	SectionUID    *string
	CheckUID      *string
	IncidentUID   *string
	Limit         int
	Offset        int
}
