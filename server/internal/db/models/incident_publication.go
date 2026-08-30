package models

import (
	"time"

	"github.com/google/uuid"
)

// PublicationState is the customer-facing lifecycle state of an incident
// publication. It is deliberately a SEPARATE vocabulary from
// models.IncidentState: the operational incident is active/resolved, while the
// public narrative walks investigating → identified → monitoring → resolved
// (the StatusUpdateKind vocabulary customers already see on the timeline).
type PublicationState string

// PublicationState values.
const (
	// PublicationStateInvestigating is the opening state of every publication.
	PublicationStateInvestigating PublicationState = "investigating"
	// PublicationStateIdentified means the cause is known, work is ongoing.
	PublicationStateIdentified PublicationState = "identified"
	// PublicationStateMonitoring means a fix is in place and being watched.
	PublicationStateMonitoring PublicationState = "monitoring"
	// PublicationStateResolved closes the publication.
	PublicationStateResolved PublicationState = "resolved"
)

// IsValid reports whether the state is one of the four recognized values.
func (s PublicationState) IsValid() bool {
	switch s {
	case PublicationStateInvestigating, PublicationStateIdentified,
		PublicationStateMonitoring, PublicationStateResolved:
		return true
	default:
		return false
	}
}

// UpdateKind maps a publication state onto the StatusUpdateKind used for the
// narrative row posted alongside a state change. The two vocabularies are
// intentionally identical in wording so a reader never sees the timeline and
// the incident header disagree.
func (s PublicationState) UpdateKind() StatusUpdateKind {
	switch s {
	case PublicationStateIdentified:
		return StatusUpdateKindIdentified
	case PublicationStateMonitoring:
		return StatusUpdateKindMonitoring
	case PublicationStateResolved:
		return StatusUpdateKindResolved
	case PublicationStateInvestigating:
		return StatusUpdateKindInvestigating
	default:
		return StatusUpdateKindInvestigating
	}
}

// PublicationSeverity is the public badge severity. It is display-only: it
// never routes, pages, or gates anything. Empty/NULL means "no badge".
type PublicationSeverity string

// PublicationSeverity values.
const (
	// PublicationSeverityMinor is a limited-impact issue.
	PublicationSeverityMinor PublicationSeverity = "minor"
	// PublicationSeverityMajor is a broad-impact issue.
	PublicationSeverityMajor PublicationSeverity = "major"
	// PublicationSeverityCritical is a full outage.
	PublicationSeverityCritical PublicationSeverity = "critical"
)

// IsValid reports whether the severity is one of the three recognized values.
func (s PublicationSeverity) IsValid() bool {
	switch s {
	case PublicationSeverityMinor, PublicationSeverityMajor, PublicationSeverityCritical:
		return true
	default:
		return false
	}
}

// AutoResolvePolicy decides what an auto-created publication does when its
// backing incident resolves.
type AutoResolvePolicy string

// AutoResolvePolicy values.
const (
	// AutoResolveAlways resolves the publication regardless of human edits.
	AutoResolveAlways AutoResolvePolicy = "always"
	// AutoResolveIfUntouched (default) resolves only while nobody has edited
	// the publication; once a human owns the narrative, the automation posts a
	// "component recovered" monitoring note and leaves the final resolve to
	// them.
	AutoResolveIfUntouched AutoResolvePolicy = "if_untouched"
	// AutoResolveNever posts nothing and resolves nothing.
	AutoResolveNever AutoResolvePolicy = "never"
)

// IsValid reports whether the policy is one of the three recognized values.
func (p AutoResolvePolicy) IsValid() bool {
	switch p {
	case AutoResolveAlways, AutoResolveIfUntouched, AutoResolveNever:
		return true
	default:
		return false
	}
}

// DefaultAutoPublishDelaySeconds is the debounce a new page starts with: an
// incident must still be open a minute later before customers hear about it.
const DefaultAutoPublishDelaySeconds = 60

// DefaultPublicationNotifyCap is how many subscriber fan-out waves one
// publication may trigger per hour before further updates are posted silently.
// Overridable per org via the `status_page.publication_notify_cap` parameter.
const DefaultPublicationNotifyCap = 4

// IncidentPublication is the publication overlay: "this incident is visible on
// this status page, under this customer-readable title, in this state".
//
// It is deliberately NOT the incident row. The operational `incidents` row
// carries ack/snooze metadata, auto-generated internal titles and probe
// diagnostics (`details`), none of which may ever reach a customer. Everything
// on this struct is safe to render publicly EXCEPT the two notify-window
// counters, which are internal bookkeeping.
type IncidentPublication struct {
	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	// IncidentUID links the publication to the operational incident. NULL
	// means the publication was authored by hand and tracks nothing.
	IncidentUID   *string `bun:"incident_uid"`
	StatusPageUID string  `bun:"status_page_uid,notnull"`
	// PublicTitle is templated at creation from the resource's PUBLIC display
	// name and is freely editable afterwards. It is never derived from the
	// incident title, which is internal.
	PublicTitle string           `bun:"public_title,notnull"`
	PublicState PublicationState `bun:"public_state,notnull"`
	// Severity is the display-only public badge. nil = no badge.
	Severity *string `bun:"severity"`
	// AutoCreated marks a publication minted by the auto-publish pipeline.
	// Only auto-created rows are candidates for auto-resolve and relapse
	// reopen; a hand-authored publication is always the human's to close.
	AutoCreated bool `bun:"auto_created,notnull"`
	// HumanTouchedAt is stamped the first time a person edits the publication
	// or posts an update on it. It is the whole basis of the `if_untouched`
	// auto-resolve policy.
	HumanTouchedAt *time.Time `bun:"human_touched_at"`
	PublishedAt    time.Time  `bun:"published_at,notnull,default:current_timestamp"`
	ResolvedAt     *time.Time `bun:"resolved_at"`
	// NotifyWindowStart / NotifyWindowCount implement the per-publication
	// subscriber storm cap: at most N fan-out waves per rolling hour. Internal
	// bookkeeping — never serialized publicly.
	NotifyWindowStart *time.Time `bun:"notify_window_start"`
	NotifyWindowCount int        `bun:"notify_window_count,notnull"`
	CreatedAt         time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt         time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt         *time.Time `bun:"deleted_at"`
}

// NewIncidentPublication builds a publication row with a generated UID, opening
// in the `investigating` state.
func NewIncidentPublication(orgUID, statusPageUID, title string, now time.Time) *IncidentPublication {
	return &IncidentPublication{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		StatusPageUID:   statusPageUID,
		PublicTitle:     title,
		PublicState:     PublicationStateInvestigating,
		PublishedAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// IsResolved reports whether the publication has been closed.
func (p *IncidentPublication) IsResolved() bool {
	return p.PublicState == PublicationStateResolved || p.ResolvedAt != nil
}

// IncidentPublicationUpdate is the tri-state patch applied to a publication
// row. A nil pointer leaves the column untouched; a Clear* flag writes NULL.
type IncidentPublicationUpdate struct {
	PublicTitle     *string
	PublicState     *PublicationState
	Severity        *string
	ClearSeverity   bool
	HumanTouchedAt  *time.Time
	ResolvedAt      *time.Time
	ClearResolvedAt bool
	// NotifyWindowStart / NotifyWindowCount are written together by the storm
	// cap; a nil NotifyWindowStart with NotifyWindowCount set is not a valid
	// combination and is ignored.
	NotifyWindowStart *time.Time
	NotifyWindowCount *int
}

// ListIncidentPublicationsFilter narrows a publication listing.
type ListIncidentPublicationsFilter struct {
	OrganizationUID string
	StatusPageUID   string
	IncidentUID     string
	// State filters to a single public state when non-empty.
	State string
	// ActiveOnly restricts to publications that are not resolved.
	ActiveOnly bool
	Limit      int
	Offset     int
}
