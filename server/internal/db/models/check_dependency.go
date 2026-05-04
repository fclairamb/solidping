package models

import (
	"time"

	"github.com/google/uuid"
)

// CheckDependencyKind enumerates whether a parent failure is a hard or soft
// cause for the child. Hard edges drive rollup (paging suppression); soft
// edges are informational only.
type CheckDependencyKind string

const (
	// CheckDependencyKindHard means the child reliably fails when the parent does.
	CheckDependencyKindHard CheckDependencyKind = "hard"
	// CheckDependencyKindSoft means the child may degrade but won't necessarily fail.
	CheckDependencyKindSoft CheckDependencyKind = "soft"
)

// IsValid reports whether the value is one of the known kinds.
func (k CheckDependencyKind) IsValid() bool {
	return k == CheckDependencyKindHard || k == CheckDependencyKindSoft
}

// CheckDependency is a directed edge (parent → child) inside an org's
// dependency DAG. The same org constraint is enforced at write time.
type CheckDependency struct {
	UID             string              `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string              `bun:"organization_uid,notnull"`
	ParentCheckUID  string              `bun:"parent_check_uid,notnull"`
	ChildCheckUID   string              `bun:"child_check_uid,notnull"`
	Kind            CheckDependencyKind `bun:"kind,notnull"`
	Description     *string             `bun:"description"`
	CreatedAt       time.Time           `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time           `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time          `bun:"deleted_at"`
}

// NewCheckDependency builds a fresh edge.
func NewCheckDependency(
	orgUID, parentUID, childUID string,
	kind CheckDependencyKind,
	description *string,
) *CheckDependency {
	now := time.Now()

	return &CheckDependency{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		ParentCheckUID:  parentUID,
		ChildCheckUID:   childUID,
		Kind:            kind,
		Description:     description,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// CheckDependencyUpdate captures the writable fields. Pointer = optional.
type CheckDependencyUpdate struct {
	Kind             *CheckDependencyKind
	Description      *string
	ClearDescription bool
}
