package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// CheckVersionStatus is the lifecycle state of a check_versions row
// (spec 2026-10-03-06).
type CheckVersionStatus string

const (
	// CheckVersionStatusApplied is a definition that was (or is) live.
	CheckVersionStatusApplied CheckVersionStatus = "applied"
	// CheckVersionStatusProposed is a definition awaiting approval.
	CheckVersionStatusProposed CheckVersionStatus = "proposed"
	// CheckVersionStatusRejected is a refused proposal.
	CheckVersionStatusRejected CheckVersionStatus = "rejected"
)

// CheckVersionOrigin says where a check change came from.
type CheckVersionOrigin string

const (
	// CheckVersionOriginUser is a change made from the dashboard (a user session).
	CheckVersionOriginUser CheckVersionOrigin = "user"
	// CheckVersionOriginAPI is a change made with an API token.
	CheckVersionOriginAPI CheckVersionOrigin = "api"
	// CheckVersionOriginApply is a config-as-code apply or import.
	CheckVersionOriginApply CheckVersionOrigin = "apply"
	// CheckVersionOriginMCP is a change made through the MCP server.
	CheckVersionOriginMCP CheckVersionOrigin = "mcp"
	// CheckVersionOriginSystem is a change with no caller (jobs, migrations).
	CheckVersionOriginSystem CheckVersionOrigin = "system"
	// CheckVersionOriginAIGenerate is a definition written by an AI generator.
	CheckVersionOriginAIGenerate CheckVersionOrigin = "ai_generate"
	// CheckVersionOriginAIRepair is a repair proposed by an AI.
	CheckVersionOriginAIRepair CheckVersionOrigin = "ai_repair"
)

// CheckVersionRetention is how many applied versions are kept per check.
// Older applied versions are pruned when a new one is recorded.
const CheckVersionRetention = 100

// CheckVersion is one recorded definition of a check (spec 2026-10-03-06).
// The snapshot never holds secrets: see the checkversion package.
type CheckVersion struct {
	bun.BaseModel `bun:"table:check_versions,alias:check_version"`

	UID             string             `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string             `bun:"organization_uid,notnull"`
	CheckUID        string             `bun:"check_uid,notnull"`
	Version         int                `bun:"version,notnull"`
	Snapshot        JSONMap            `bun:"snapshot,type:jsonb,notnull"`
	SnapshotHash    string             `bun:"snapshot_hash,notnull"`
	Status          CheckVersionStatus `bun:"status,notnull"`
	BaseVersion     *int               `bun:"base_version"`
	Origin          CheckVersionOrigin `bun:"origin,notnull"`
	ActorUserUID    *string            `bun:"actor_user_uid"`
	Reason          *string            `bun:"reason"`
	DecidedByUID    *string            `bun:"decided_by_user_uid"`
	DecidedAt       *time.Time         `bun:"decided_at"`
	CreatedAt       time.Time          `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time          `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewCheckVersion builds an applied version row with a fresh UID. The caller
// sets Version.
func NewCheckVersion(orgUID, checkUID string, snapshot JSONMap, hash string) *CheckVersion {
	now := time.Now()

	return &CheckVersion{
		UID:             uuid.Must(uuid.NewV7()).String(),
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		Snapshot:        snapshot,
		SnapshotHash:    hash,
		Status:          CheckVersionStatusApplied,
		Origin:          CheckVersionOriginSystem,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
