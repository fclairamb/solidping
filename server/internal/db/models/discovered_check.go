package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// DiscoverySource identifies which discovery mechanism produced a suggested
// check. It is a named string (matching the ProviderType / JobType convention)
// so the set of sources is closed in code while staying a plain TEXT column.
type DiscoverySource string

// Supported discovery sources. Additional sources (container, kubernetes) are
// registered by their own specs; the column has no DB CHECK constraint so new
// values are Go-level only.
const (
	// DiscoverySourceLAN marks a check suggested by the CIDR network scanner.
	DiscoverySourceLAN DiscoverySource = "lan"
	// DiscoverySourceFreebox marks a check suggested via the Freebox LAN browser.
	DiscoverySourceFreebox DiscoverySource = "freebox"
)

// DiscoveredCheck is one suggested check produced by a discovery scan. Rows are
// grouped for display by GroupKey (an IP, container ID, workload UID…) — the
// group is purely a rendering concern; the stored unit is the check itself.
type DiscoveredCheck struct {
	UID             string          `bun:"uid,pk,type:varchar(36)" json:"uid"`
	OrganizationUID string          `bun:"organization_uid,notnull" json:"organizationUid"`
	JobUID          string          `bun:"job_uid,notnull" json:"jobUid"` // the scan (plan UID for chunked)
	Source          DiscoverySource `bun:"source,notnull" json:"source"`  // lan|freebox|container|kubernetes

	GroupKey   string `bun:"group_key,notnull" json:"groupKey"`     // stable grouping identity
	GroupLabel string `bun:"group_label,notnull" json:"groupLabel"` // display label

	Name   string          `bun:"name,notnull" json:"name"`        // suggested check name
	Slug   string          `bun:"slug,notnull" json:"slug"`        // suggested check slug (unique per group)
	Type   string          `bun:"type,notnull" json:"type"`        // check type: http|tcp|icmp|docker|kubernetes…
	Config json.RawMessage `bun:"config,type:jsonb" json:"config"` // the check config

	Metadata json.RawMessage `bun:"metadata,type:jsonb" json:"metadata,omitempty"` // group-display hints (denormalized)

	PromotedToCheckUID *string    `bun:"promoted_to_check_uid" json:"promotedToCheckUid,omitempty"` //nolint:lll
	DiscoveredAt       time.Time  `bun:"discovered_at,notnull,default:current_timestamp" json:"discoveredAt"`
	CreatedAt          time.Time  `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
	UpdatedAt          time.Time  `bun:"updated_at,notnull,default:current_timestamp" json:"updatedAt"`
	DeletedAt          *time.Time `bun:"deleted_at,soft_delete" json:"deletedAt,omitempty"`
}

// NewDiscoveredCheck builds a DiscoveredCheck row from the fully-formed pieces a
// suggester emits within a group. config and meta may be nil; config defaults to
// "{}" so the NOT NULL column is satisfied.
func NewDiscoveredCheck(
	orgUID, jobUID string, source DiscoverySource,
	groupKey, groupLabel, name, slug, checkType string,
	config, meta json.RawMessage,
) *DiscoveredCheck {
	if len(config) == 0 {
		config = json.RawMessage("{}")
	}

	now := time.Now()

	return &DiscoveredCheck{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		JobUID:          jobUID,
		Source:          source,
		GroupKey:        groupKey,
		GroupLabel:      groupLabel,
		Name:            name,
		Slug:            slug,
		Type:            checkType,
		Config:          config,
		Metadata:        meta,
		DiscoveredAt:    now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
