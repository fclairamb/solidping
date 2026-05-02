package models

import (
	"time"

	"github.com/google/uuid"
)

// EscalationTargetType is the kind of recipient a policy step pages.
type EscalationTargetType string

const (
	// EscalationTargetUser pages a specific user via their preferred channels.
	EscalationTargetUser EscalationTargetType = "user"
	// EscalationTargetSchedule pages whoever the on-call resolver returns at fire time.
	EscalationTargetSchedule EscalationTargetType = "schedule"
	// EscalationTargetConnection fires a specific notification connection.
	EscalationTargetConnection EscalationTargetType = "connection"
	// EscalationTargetAllAdmins pages every admin member of the organization.
	EscalationTargetAllAdmins EscalationTargetType = "all_admins"
)

// EscalationPolicy is a reusable orchestration of paging steps. Distinct
// from check_connections (per-check broadcast). The check or its group
// references one policy via escalation_policy_uid.
type EscalationPolicy struct {
	UID                string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID    string     `bun:"organization_uid,notnull"`
	Slug               string     `bun:"slug,notnull"`
	Name               string     `bun:"name,notnull"`
	Description        *string    `bun:"description"`
	RepeatMax          int        `bun:"repeat_max,notnull,default:0"`
	RepeatAfterMinutes *int       `bun:"repeat_after_minutes"`
	CreatedAt          time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt          time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt          *time.Time `bun:"deleted_at"`
}

// NewEscalationPolicy builds a policy with a fresh UID.
func NewEscalationPolicy(orgUID, slug, name string) *EscalationPolicy {
	now := time.Now()

	return &EscalationPolicy{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Slug:            slug,
		Name:            name,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// EscalationPolicyUpdate captures the writable fields. Pointer = optional.
type EscalationPolicyUpdate struct {
	Slug               *string
	Name               *string
	Description        *string
	RepeatMax          *int
	RepeatAfterMinutes *int

	ClearDescription        bool
	ClearRepeatAfterMinutes bool
}

// EscalationPolicyStep is one rung of a policy. Delays are between adjacent
// steps (see spec): inserting a step in the middle does not require
// recomputing downstream delays.
type EscalationPolicyStep struct {
	UID          string    `bun:"uid,pk,type:varchar(36)"`
	PolicyUID    string    `bun:"policy_uid,notnull"`
	Position     int       `bun:"position,notnull"`
	DelayMinutes int       `bun:"delay_minutes,notnull,default:0"`
	CreatedAt    time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt    time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewEscalationPolicyStep builds a step row with a fresh UID.
func NewEscalationPolicyStep(policyUID string, position, delayMinutes int) *EscalationPolicyStep {
	now := time.Now()

	return &EscalationPolicyStep{
		UID:          uuid.New().String(),
		PolicyUID:    policyUID,
		Position:     position,
		DelayMinutes: delayMinutes,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// EscalationPolicyTarget is one recipient inside a step. Multiple targets
// per step fire in parallel.
type EscalationPolicyTarget struct {
	UID        string               `bun:"uid,pk,type:varchar(36)"`
	StepUID    string               `bun:"step_uid,notnull"`
	TargetType EscalationTargetType `bun:"target_type,notnull"`
	TargetUID  *string              `bun:"target_uid"`
	Position   int                  `bun:"position,notnull,default:0"`
}

// NewEscalationPolicyTarget builds a target row with a fresh UID.
func NewEscalationPolicyTarget(
	stepUID string, targetType EscalationTargetType, targetUID *string, position int,
) *EscalationPolicyTarget {
	return &EscalationPolicyTarget{
		UID:        uuid.New().String(),
		StepUID:    stepUID,
		TargetType: targetType,
		TargetUID:  targetUID,
		Position:   position,
	}
}
