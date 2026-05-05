package models

import (
	"time"

	"github.com/google/uuid"
)

// EntitlementSource identifies who wrote the row.
type EntitlementSource string

// Entitlement source labels.
const (
	EntitlementSourceDefault    EntitlementSource = "default"
	EntitlementSourceSelfHosted EntitlementSource = "self-hosted"
	EntitlementSourceAdmin      EntitlementSource = "admin"
	EntitlementSourceBilling    EntitlementSource = "billing-service"
)

// OrgEntitlements is one row per org. NULL on a limit means unlimited;
// NULL on a feature means "use the in-code default". Defaults are merged
// at read time by the entitlements service, never stored.
type OrgEntitlements struct {
	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull,unique"`

	MaxChecks               *int `bun:"max_checks"`
	MaxMembers              *int `bun:"max_members"`
	MaxStatusPages          *int `bun:"max_status_pages"`
	MaxCheckGroups          *int `bun:"max_check_groups"`
	MaxMaintenanceWindows   *int `bun:"max_maintenance_windows"`
	MaxConnections          *int `bun:"max_connections"`
	MaxWorkers              *int `bun:"max_workers"`
	MaxAPITokens            *int `bun:"max_api_tokens"`
	RetentionDaysRaw        *int `bun:"retention_days_raw"`
	RetentionDaysAggregated *int `bun:"retention_days_aggregated"`
	MinCheckPeriodSeconds   *int `bun:"min_check_period_seconds"`

	FeatureSSO             *bool `bun:"feature_sso"`
	FeatureMCP             *bool `bun:"feature_mcp"`
	FeatureCustomBranding  *bool `bun:"feature_custom_branding"`
	FeaturePrioritySupport *bool `bun:"feature_priority_support"`
	FeatureMultiRegion     *bool `bun:"feature_multi_region"`
	FeatureAdvancedAlerts  *bool `bun:"feature_advanced_alerts"`

	// AllowedCheckTypes is stored JSON-encoded for cross-DB portability.
	// nil/empty string = all check types allowed.
	AllowedCheckTypes *string `bun:"allowed_check_types"`

	Source      EntitlementSource `bun:"source,notnull"`
	DisplayName *string           `bun:"display_name"`
	ExternalRef *string           `bun:"external_ref"`
	Metadata    JSONMap           `bun:"metadata,type:jsonb,nullzero"`

	ExpiresAt    *time.Time `bun:"expires_at"`
	LastSyncedAt *time.Time `bun:"last_synced_at"`
	CreatedAt    time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt    time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewOrgEntitlements builds a fresh row with the given source. All
// limit/feature fields are left nil so the resolver merges in defaults.
func NewOrgEntitlements(orgUID string, source EntitlementSource) *OrgEntitlements {
	now := time.Now()

	return &OrgEntitlements{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Source:          source,
		Metadata:        make(JSONMap),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// OrgEntitlementAudit records one write to org_entitlements. The before
// snapshot is nil on the first row for an org; after is always populated.
type OrgEntitlementAudit struct {
	UID             string    `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string    `bun:"organization_uid,notnull"`
	Source          string    `bun:"source,notnull"`
	Actor           string    `bun:"actor,notnull"`
	BeforeSnapshot  JSONMap   `bun:"before_snapshot,type:jsonb,nullzero"`
	AfterSnapshot   JSONMap   `bun:"after_snapshot,type:jsonb,notnull"`
	Reason          *string   `bun:"reason"`
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// NewOrgEntitlementAudit builds a fresh audit row.
func NewOrgEntitlementAudit(
	orgUID, source, actor string,
	before JSONMap, after JSONMap, reason *string,
) *OrgEntitlementAudit {
	return &OrgEntitlementAudit{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Source:          source,
		Actor:           actor,
		BeforeSnapshot:  before,
		AfterSnapshot:   after,
		Reason:          reason,
		CreatedAt:       time.Now(),
	}
}

// ListOrgEntitlementAuditsFilter narrows an audit list query.
type ListOrgEntitlementAuditsFilter struct {
	OrganizationUID string
	Limit           int
	Offset          int
}
