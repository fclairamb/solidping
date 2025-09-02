package models

import (
	"time"

	"github.com/google/uuid"
)

// Organization represents a tenant in the system.
type Organization struct {
	UID       string     `bun:"uid,pk,type:varchar(36)"`
	Slug      string     `bun:"slug,notnull"`
	Name      string     `bun:"name"`
	CreatedAt time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt *time.Time `bun:"deleted_at"`
}

// NewOrganization creates a new organization with generated UID.
func NewOrganization(slug, name string) *Organization {
	now := time.Now()

	return &Organization{
		UID:       uuid.New().String(),
		Slug:      slug,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// OrganizationUpdate represents fields that can be updated.
type OrganizationUpdate struct {
	Slug *string
	Name *string
}
