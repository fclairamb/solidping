// Package entitlements owns per-org limits and feature toggles.
//
// The OSS knows nothing about plan SKUs, prices, trials, or invoices —
// those live in a separate billing service. This package stores raw
// numbers and booleans, exposes them via HTTP, and (in follow-up PRs)
// enforces them at every create-* boundary.
//
// Limits use NULL/nil to mean "unlimited"; features use NULL/nil to mean
// "use the in-code default". This lets self-hosted operators stay
// effectively unbounded without picking a magic sentinel that someone
// will eventually reach.
package entitlements

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Limits aliases the model-layer struct so the public API is stable
// across the JSONB-collapse refactor. nil = unlimited.
type Limits = models.EntitlementLimits

// Features aliases the model-layer struct. nil = use default.
type Features = models.EntitlementFeatures

// Entitlements is the full input/output shape used by the API and the
// audit log. On disk, Limits/Features/AllowedCheckTypes/DisplayName
// live inside the row's `payload` JSONB column; Source/ExternalRef/
// Metadata/ExpiresAt/LastSyncedAt are break-out columns. The wire
// format is preserved across that split.
type Entitlements struct {
	Limits            Limits                   `json:"limits"`
	Features          Features                 `json:"features"`
	AllowedCheckTypes []string                 `json:"allowedCheckTypes,omitempty"`
	Source            models.EntitlementSource `json:"source"`
	DisplayName       *string                  `json:"displayName,omitempty"`
	ExternalRef       *string                  `json:"externalRef,omitempty"`
	Metadata          map[string]any           `json:"metadata,omitempty"`
	ExpiresAt         *time.Time               `json:"expiresAt,omitempty"`
	LastSyncedAt      *time.Time               `json:"lastSyncedAt,omitempty"`
}

// Resolved is the resolver output: defaults merged with the stored row,
// plus live usage and the stale flag. NULL fields in the row are
// replaced by the corresponding default. Returned to the API and to
// /api/v1/features for frontend rendering.
type Resolved struct {
	Limits            Limits                   `json:"limits"`
	Features          Features                 `json:"features"`
	AllowedCheckTypes []string                 `json:"allowedCheckTypes,omitempty"`
	Usage             Usage                    `json:"usage"`
	Source            models.EntitlementSource `json:"source"`
	DisplayName       *string                  `json:"displayName,omitempty"`
	ExpiresAt         *time.Time               `json:"expiresAt,omitempty"`
	LastSyncedAt      *time.Time               `json:"lastSyncedAt,omitempty"`
	Stale             bool                     `json:"stale"`
}

// Usage counts non-deleted resources for the org. Computed live by the
// resolver; not stored.
type Usage struct {
	Checks      int `json:"checks"`
	Members     int `json:"members"`
	StatusPages int `json:"statusPages"`
	CheckGroups int `json:"checkGroups"`
	Connections int `json:"connections"`
}

// Bool is a tiny helper for default-defining boolean pointers.
func Bool(b bool) *bool { return &b }

// Int is a tiny helper for default-defining int pointers.
func Int(i int) *int { return &i }

// DefaultEntitlements is the in-memory seed for a fresh org. SaaS
// deployments override these via system parameters at startup so the
// OSS code itself is identical between self-hosted and SaaS.
//
//nolint:gochecknoglobals // intentional package-level default seed
var DefaultEntitlements = Entitlements{
	Limits: Limits{
		MaxChecks:               nil,
		MaxMembers:              nil,
		MaxStatusPages:          nil,
		MaxCheckGroups:          nil,
		MaxMaintenanceWindows:   nil,
		MaxConnections:          nil,
		MaxWorkers:              nil,
		MaxAPITokens:            nil,
		RetentionDaysRaw:        Int(30),
		RetentionDaysAggregated: Int(365),
		MinCheckPeriodSeconds:   Int(30),
	},
	Features: Features{
		SSO:             Bool(true),
		MCP:             Bool(true),
		CustomBranding:  Bool(true),
		PrioritySupport: Bool(false),
		MultiRegion:     Bool(true),
		AdvancedAlerts:  Bool(true),
	},
	Source: models.EntitlementSourceDefault,
}
