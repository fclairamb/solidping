package checks

import "time"

// Spec 2026-11-21-01: the export / apply document carries three more resource
// kinds next to `checks`. Each is optional; an absent or empty array means
// "this manifest does not manage that kind" (apply never prunes a kind the
// manifest does not mention). Cross-references are by slug and resolved after
// the checks of the same manifest.

// Plan entry kinds.
const (
	KindCheck             = "check"
	KindChannel           = "channel"
	KindMaintenanceWindow = "maintenanceWindow"
	KindStatusPage        = "statusPage"
)

// ExportChannel is a notification channel (integration) in a manifest. It is
// matched on Slug, which apply stamps on the row. Secret settings are
// references (`${env:NAME}`, `${param:key}`), resolved when the manifest is
// applied; an export never carries a secret value.
type ExportChannel struct {
	Slug      string         `json:"slug"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Enabled   *bool          `json:"enabled,omitempty"`
	IsDefault *bool          `json:"isDefault,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// ExportMaintenanceWindow is a maintenance window in a manifest, matched on
// Slug. Checks lists the slugs of the checks it covers.
type ExportMaintenanceWindow struct {
	Slug          string     `json:"slug"`
	Title         string     `json:"title"`
	Description   string     `json:"description,omitempty"`
	StartAt       time.Time  `json:"startAt"`
	EndAt         time.Time  `json:"endAt"`
	Recurrence    string     `json:"recurrence,omitempty"`
	RecurrenceEnd *time.Time `json:"recurrenceEnd,omitempty"`
	Checks        []string   `json:"checks,omitempty"`
}

// ExportStatusPage is a status page in a manifest, matched on Slug. A nil
// Sections leaves the page's sections alone; a non-nil list is authoritative
// (sections absent from it are removed).
type ExportStatusPage struct {
	Slug             string                    `json:"slug"`
	Name             string                    `json:"name"`
	Description      string                    `json:"description,omitempty"`
	Visibility       string                    `json:"visibility,omitempty"`
	Enabled          *bool                     `json:"enabled,omitempty"`
	IsDefault        *bool                     `json:"isDefault,omitempty"`
	ShowAvailability *bool                     `json:"showAvailability,omitempty"`
	ShowResponseTime *bool                     `json:"showResponseTime,omitempty"`
	Language         string                    `json:"language,omitempty"`
	Sections         []ExportStatusPageSection `json:"sections,omitempty"`
}

// ExportStatusPageSection is one section of a status page; Checks are check
// slugs, in display order.
type ExportStatusPageSection struct {
	Slug   string   `json:"slug"`
	Name   string   `json:"name"`
	Checks []string `json:"checks,omitempty"`
}

// HasOnlyChecks reports whether the document carries no kind besides checks.
func (d *ExportDocument) HasOnlyChecks() bool {
	return len(d.Channels) == 0 && len(d.MaintenanceWindows) == 0 && len(d.StatusPages) == 0
}

// IsEmpty reports whether every kind of the document is empty.
func (d *ExportDocument) IsEmpty() bool {
	return len(d.Checks) == 0 && d.HasOnlyChecks()
}
