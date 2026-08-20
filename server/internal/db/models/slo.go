package models

import (
	"time"

	"github.com/google/uuid"
)

// SLO represents a service-level objective over one check or one check group.
//
// Windows are calendar months in Timezone and nothing per-window is stored:
// attainment, error budget and history are always recomputed at read time off
// the permanent `month` rollups. The emailed uptime report is the only frozen
// artifact.
type SLO struct {
	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	Name            string `bun:"name,notnull"`
	Slug            string `bun:"slug,notnull"`
	// Exactly one of CheckUID / CheckGroupUID is set — enforced by the
	// slos_scope_xor CHECK constraint, not merely by convention.
	CheckUID      *string `bun:"check_uid"`
	CheckGroupUID *string `bun:"check_group_uid"`
	// TargetPct is the objective, e.g. 99.9. 0 < TargetPct <= 100.
	TargetPct float64 `bun:"target_pct,notnull"`
	// Timezone is the IANA zone the calendar month is resolved in.
	Timezone string `bun:"timezone,notnull,default:'UTC'"`
	// ExcludeMaintenance subtracts probes tagged results.maintenance from this
	// SLO's denominator. It affects nothing outside the SLO read path.
	ExcludeMaintenance bool       `bun:"exclude_maintenance,notnull"`
	Enabled            bool       `bun:"enabled,notnull"`
	CreatedAt          time.Time  `bun:"created_at,notnull"`
	UpdatedAt          time.Time  `bun:"updated_at,notnull"`
	DeletedAt          *time.Time `bun:"deleted_at"`
}

// DefaultSLOTimezone is the timezone a new SLO gets when the caller supplies none.
const DefaultSLOTimezone = "UTC"

// NewSLO builds an SLO with a generated UID and the default scalars.
// The caller sets exactly one of CheckUID / CheckGroupUID.
func NewSLO(orgUID, name, slug string, targetPct float64) *SLO {
	now := time.Now()

	return &SLO{
		UID:                uuid.New().String(),
		OrganizationUID:    orgUID,
		Name:               name,
		Slug:               slug,
		TargetPct:          targetPct,
		Timezone:           DefaultSLOTimezone,
		ExcludeMaintenance: true,
		Enabled:            true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

// SLOUpdate carries the fields a PATCH may change. Nil means "leave alone".
type SLOUpdate struct {
	Name               *string
	Slug               *string
	CheckUID           *string
	CheckGroupUID      *string
	TargetPct          *float64
	Timezone           *string
	ExcludeMaintenance *bool
	Enabled            *bool
}

// ListSLOsFilter provides filtering options for listing SLOs.
type ListSLOsFilter struct {
	// CheckUID, when set, restricts to SLOs scoped directly to that check.
	CheckUID string
	// EnabledOnly restricts to enabled SLOs.
	EnabledOnly bool
	Limit       int
}
