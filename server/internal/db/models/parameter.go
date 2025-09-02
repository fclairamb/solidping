package models

import (
	"time"

	"github.com/google/uuid"
)

// Parameter represents a key-value configuration.
// When OrganizationUID is nil, this is a system-wide parameter.
type Parameter struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID *string    `bun:"organization_uid"`
	Key             string     `bun:"key,notnull"`
	Value           JSONMap    `bun:"value,type:jsonb,notnull"`
	Secret          *bool      `bun:"secret"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewParameter creates a new organization-scoped parameter with generated UID.
func NewParameter(orgUID, key string, value JSONMap) *Parameter {
	now := time.Now()

	return &Parameter{
		UID:             uuid.New().String(),
		OrganizationUID: &orgUID,
		Key:             key,
		Value:           value,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// NewSystemParameter creates a new system-wide parameter (organization_uid = nil).
func NewSystemParameter(key string, value JSONMap, secret bool) *Parameter {
	now := time.Now()

	return &Parameter{
		UID:       uuid.New().String(),
		Key:       key,
		Value:     value,
		Secret:    &secret,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ParameterUpdate represents fields that can be updated.
type ParameterUpdate struct {
	Key    *string
	Value  *JSONMap
	Secret *bool
}
