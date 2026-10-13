package models

import (
	"time"

	"github.com/google/uuid"
)

// Parameter represents a key-value configuration.
// When OrganizationUID is nil, this is a system-wide parameter.
type Parameter struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID *string    `bun:"organization_uid"`
	Key             string     `bun:"key,notnull"`
	Value           JSONMap    `bun:"value,type:jsonb,notnull"`
	Secret          *bool      `bun:"secret"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewParameter creates a new organization-scoped parameter with generated UID.
func NewParameter(orgUID, key string, value JSONMap) *Parameter {
	now := time.Now()

	return &Parameter{
		UID:             uuid.New().String(),
		OrganizationUID: &orgUID,
		Key:             key,
		Value:           value,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// NewSystemParameter creates a new system-wide parameter (organization_uid = nil).
func NewSystemParameter(key string, value JSONMap, secret bool) *Parameter {
	now := time.Now()

	return &Parameter{
		UID:       uuid.New().String(),
		Key:       key,
		Value:     value,
		Secret:    &secret,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ParameterValueKey is the single JSON key a scalar parameter value is stored
// under. Both engines write and read `{"value": …}`; naming it once keeps them
// from ever drifting.
const ParameterValueKey = "value"

// ParamKeyTracerouteEnabled is the ORG-scoped switch that supplies the default
// for every check whose own `traceroute_on_failure` is NULL (spec
// 2026-08-21-10). Absent means ON.
//
// It lives here, next to the parameter machinery, rather than in the incident
// package that reads it: the org-settings handler writes it and the incident
// pipeline reads it, and those two packages sit on opposite sides of an import
// cycle. A shared constant is what keeps the two ends from drifting apart into
// two subtly different strings.
const ParamKeyTracerouteEnabled = "diagnostics.traceroute.enabled"

// ParamKeyIncidentReminderInterval is the ORG-scoped default reminder interval,
// in seconds, for every check whose own `reminder_interval_seconds` is NULL
// (spec 2026-10-12-16). Absent means 0, which is off: nobody gets new noise
// after an upgrade. Lives here for the same import-cycle reason as
// ParamKeyTracerouteEnabled (the org-settings handler writes it, jobtypes reads
// it).
const ParamKeyIncidentReminderInterval = "incidents.reminder_interval_seconds"

// Reminder interval bounds (spec 2026-10-12-16): 0 is off, otherwise the
// interval must sit between 15 minutes and 24 hours.
const (
	ReminderIntervalMinSeconds = 900
	ReminderIntervalMaxSeconds = 86400
)

// ValidReminderIntervalSeconds reports whether a reminder interval is
// storable: 0 (off) or ReminderIntervalMinSeconds..ReminderIntervalMaxSeconds.
func ValidReminderIntervalSeconds(seconds int) bool {
	return seconds == 0 || (seconds >= ReminderIntervalMinSeconds && seconds <= ReminderIntervalMaxSeconds)
}

// ParamKeyStatusPageAllowedEmbedOrigins is the ORG-scoped allowlist of origins
// that may frame the org's public status pages (spec 2026-09-25-28). Stored as
// a comma-separated string of scheme+host origins. Absent means only the
// SolidPing origin itself may frame them (`frame-ancestors 'self'`).
//
// Like ParamKeyTracerouteEnabled it lives here because the org-settings
// handler writes it and the HTTP layer (internal/app) reads it.
const ParamKeyStatusPageAllowedEmbedOrigins = "statuspage.allowed_embed_origins"

// ParameterValue wraps a scalar into the JSON envelope parameters are stored
// in.
func ParameterValue(value any) JSONMap {
	return JSONMap{ParameterValueKey: value}
}

// ParameterUpdate represents fields that can be updated.
type ParameterUpdate struct {
	Key    *string
	Value  *JSONMap
	Secret *bool
}
