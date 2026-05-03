package models

import (
	"time"

	"github.com/google/uuid"
)

// MembershipRequestStatus is the lifecycle state of a membership request.
type MembershipRequestStatus string

// Membership request statuses.
const (
	MembershipRequestStatusPending   MembershipRequestStatus = "pending"
	MembershipRequestStatusApproved  MembershipRequestStatus = "approved"
	MembershipRequestStatusRejected  MembershipRequestStatus = "rejected"
	MembershipRequestStatusCancelled MembershipRequestStatus = "canceled"
)

// MembershipRequest represents a user's ask to join an organization.
type MembershipRequest struct {
	UID             string                  `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string                  `bun:"organization_uid,notnull"`
	UserUID         string                  `bun:"user_uid,notnull"`
	Message         *string                 `bun:"message"`
	Status          MembershipRequestStatus `bun:"status,notnull"`
	DecisionReason  *string                 `bun:"decision_reason"`
	DecidedAt       *time.Time              `bun:"decided_at"`
	DecidedByUID    *string                 `bun:"decided_by_uid"`
	CreatedAt       time.Time               `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time               `bun:"updated_at,notnull,default:current_timestamp"`

	Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
	User         *User         `bun:"rel:belongs-to,join:user_uid=uid"`
	DecidedBy    *User         `bun:"rel:belongs-to,join:decided_by_uid=uid"`
}

// NewMembershipRequest creates a new pending request with a generated UID.
func NewMembershipRequest(orgUID, userUID string, message *string) *MembershipRequest {
	now := time.Now()

	return &MembershipRequest{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		UserUID:         userUID,
		Message:         message,
		Status:          MembershipRequestStatusPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// ListMembershipRequestsFilter narrows a list query.
type ListMembershipRequestsFilter struct {
	OrganizationUID string
	UserUID         string
	Status          MembershipRequestStatus
	Limit           int
	Offset          int
}
