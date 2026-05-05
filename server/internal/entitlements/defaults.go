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

// Limits is the quantitative half of an entitlement set. nil = unlimited.
type Limits struct {
	MaxChecks               *int `json:"maxChecks,omitempty"`
	MaxMembers              *int `json:"maxMembers,omitempty"`
	MaxStatusPages          *int `json:"maxStatusPages,omitempty"`
	MaxCheckGroups          *int `json:"maxCheckGroups,omitempty"`
	MaxMaintenanceWindows   *int `json:"maxMaintenanceWindows,omitempty"`
	MaxConnections          *int `json:"maxConnections,omitempty"`
	MaxWorkers              *int `json:"maxWorkers,omitempty"`
	MaxAPITokens            *int `json:"maxApiTokens,omitempty"`
	RetentionDaysRaw        *int `json:"retentionDaysRaw,omitempty"`
	RetentionDaysAggregated *int `json:"retentionDaysAggregated,omitempty"`
	MinCheckPeriodSeconds   *int `json:"minCheckPeriodSeconds,omitempty"`
}

// Features is the boolean half. nil falls back to default.
type Features struct {
	SSO             *bool `json:"sso,omitempty"`
	MCP             *bool `json:"mcp,omitempty"`
	CustomBranding  *bool `json:"customBranding,omitempty"`
	PrioritySupport *bool `json:"prioritySupport,omitempty"`
	MultiRegion     *bool `json:"multiRegion,omitempty"`
	AdvancedAlerts  *bool `json:"advancedAlerts,omitempty"`
}

// Entitlements is the full input/output shape used by the API and audit
// log. Storage uses individual columns; this struct flattens them into
// the JSON the billing service writes.
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
	Checks       int `json:"checks"`
	Members      int `json:"members"`
	StatusPages  int `json:"statusPages"`
	CheckGroups  int `json:"checkGroups"`
	Connections  int `json:"connections"`
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
