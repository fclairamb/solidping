package models

import (
	"time"

	"github.com/google/uuid"
)

// CheckGroup represents a flat organizational group for checks.
type CheckGroup struct {
	UID             string  `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string  `bun:"organization_uid,notnull"`
	Name            string  `bun:"name,notnull"`
	Slug            string  `bun:"slug,notnull"`
	Description     *string `bun:"description"`
	SortOrder       int16   `bun:"sort_order,notnull,default:0"`
	// Optional escalation policy. NULL = no group-level policy.
	EscalationPolicyUID *string    `bun:"escalation_policy_uid"`
	CreatedAt           time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt           time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt           *time.Time `bun:"deleted_at"`

	// Computed field (not stored in DB)
	CheckCount int `bun:"check_count,scanonly"`
}

// NewCheckGroup creates a new check group with generated UID.
func NewCheckGroup(orgUID, name, slug string) *CheckGroup {
	now := time.Now()

	return &CheckGroup{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Name:            name,
		Slug:            slug,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// CheckGroupUpdate represents fields that can be updated on a check group.
type CheckGroupUpdate struct {
	Name                *string
	Slug                *string
	Description         *string
	SortOrder           *int16
	EscalationPolicyUID *string

	ClearEscalationPolicyUID bool
}
