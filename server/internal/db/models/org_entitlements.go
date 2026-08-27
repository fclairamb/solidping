package models

import (
	"time"

	"github.com/google/uuid"
)

// EntitlementSource identifies who wrote the row.
type EntitlementSource string

// Entitlement source labels.
//
// EntitlementSourceAdmin and EntitlementSourceOrgAdmin look alike and are NOT
// interchangeable — the difference is which door the write came through, and
// that decides whether it outranks billing:
//
//   - `admin` is minted ONLY by the instance-level superadmin editor
//     (`PUT /api/v1/system/entitlements/:org`). It is an override: it resolves
//     whole-row (nil = unlimited) and suppresses billing pushes until it is
//     explicitly released.
//   - `org-admin` is minted by the org-scoped `PUT /api/v1/orgs/:org/entitlements`
//     when an ORG admin (not a superadmin) writes it — the self-hosted
//     operator's door, gated by `entitlements.admin_writes_enabled`. It behaves
//     exactly as `admin` did before spec 2026-08-26-06: same paid plan weight,
//     ordinary null-fill resolution, and billing's next reconcile overwrites it.
//
// Collapsing the two would let any org admin on a SaaS install that never set
// SP_ENTITLEMENTS_ADMIN_WRITES grant themselves limits AND lock the billing
// service out of correcting them, which no superadmin ever authorized.
//
// Migration note: rows already stored as `admin` were written through the
// org-scoped door and will read as superadmin overrides from now on. There are
// only a handful, they are visible in the superadmin editor, and releasing one
// is a single click — so they are left alone rather than rewritten blind.
const (
	EntitlementSourceDefault    EntitlementSource = "default"
	EntitlementSourceSelfHosted EntitlementSource = "self-hosted"
	EntitlementSourceAdmin      EntitlementSource = "admin"
	EntitlementSourceOrgAdmin   EntitlementSource = "org-admin"
	EntitlementSourceBilling    EntitlementSource = "billing-service"
)

// OrgEntitlements is one row per org. Limits, features, and source live
// inside Payload; absence inside the payload means "use the in-code
// default" (resolution happens in the entitlements service, never
// stored). ExternalRef / ExpiresAt / LastSyncedAt are kept as columns
// because they are queried or indexed at the SQL level.
type OrgEntitlements struct {
	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull,unique"`

	Payload EntitlementsPayload `bun:"payload,type:jsonb,notnull"`

	ExternalRef  *string    `bun:"external_ref"`
	ExpiresAt    *time.Time `bun:"expires_at"`
	LastSyncedAt *time.Time `bun:"last_synced_at"`
	Metadata     JSONMap    `bun:"metadata,type:jsonb,nullzero"`

	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewOrgEntitlements builds a fresh row with the given source. The
// payload is initialized with the current schema version and empty
// limits/features so the resolver merges in defaults.
func NewOrgEntitlements(orgUID string, source EntitlementSource) *OrgEntitlements {
	now := time.Now()

	return &OrgEntitlements{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Payload: EntitlementsPayload{
			Version: EntitlementsPayloadVersion,
			Source:  source,
		},
		Metadata:  make(JSONMap),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// OrgEntitlementAudit records one write to org_entitlements. The before
// snapshot is nil on the first row for an org; after is always populated.
//
// The JSON tags are load-bearing: this model is serialized straight onto the
// audit-listing endpoint, and without them Go would emit Go field names while
// the OpenAPI spec (and the client generated from it) promise camelCase.
type OrgEntitlementAudit struct {
	UID             string    `bun:"uid,pk,type:varchar(36)"                      json:"uid"`
	OrganizationUID string    `bun:"organization_uid,notnull"                     json:"organizationUid"`
	Source          string    `bun:"source,notnull"                               json:"source"`
	Actor           string    `bun:"actor,notnull"                                json:"actor"`
	BeforeSnapshot  JSONMap   `bun:"before_snapshot,type:jsonb,nullzero"          json:"beforeSnapshot,omitempty"`
	AfterSnapshot   JSONMap   `bun:"after_snapshot,type:jsonb,notnull"            json:"afterSnapshot"`
	Reason          *string   `bun:"reason"                                       json:"reason,omitempty"`
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
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
