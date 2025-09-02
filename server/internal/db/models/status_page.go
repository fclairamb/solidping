package models

import (
	"time"

	"github.com/google/uuid"
)

// StatusPage represents a public status page for an organization.
type StatusPage struct {
	UID              string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID  string     `bun:"organization_uid,notnull"`
	Name             string     `bun:"name,notnull"`
	Slug             string     `bun:"slug,notnull"`
	Description      *string    `bun:"description"`
	Visibility       string     `bun:"visibility,notnull,default:'public'"`
	IsDefault        bool       `bun:"is_default,notnull,default:false"`
	Enabled          bool       `bun:"enabled,notnull,default:true"`
	ShowAvailability bool       `bun:"show_availability,notnull,default:true"`
	ShowResponseTime bool       `bun:"show_response_time,notnull,default:true"`
	HistoryDays      int        `bun:"history_days,notnull,default:90"`
	Language         *string    `bun:"language"`
	CreatedAt        time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt        time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt        *time.Time `bun:"deleted_at"`
}

// NewStatusPage creates a new status page with generated UID.
func NewStatusPage(orgUID, name, slug string) *StatusPage {
	now := time.Now()

	return &StatusPage{
		UID:              uuid.New().String(),
		OrganizationUID:  orgUID,
		Name:             name,
		Slug:             slug,
		Visibility:       "public",
		Enabled:          true,
		ShowAvailability: true,
		ShowResponseTime: true,
		HistoryDays:      90,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// StatusPageUpdate represents fields that can be updated on a status page.
type StatusPageUpdate struct {
	Name             *string
	Slug             *string
	Description      *string
	Visibility       *string
	IsDefault        *bool
	Enabled          *bool
	ShowAvailability *bool
	ShowResponseTime *bool
	HistoryDays      *int
	Language         *string
}

// StatusPageSection represents a section within a status page.
type StatusPageSection struct {
	UID           string     `bun:"uid,pk,type:varchar(36)"`
	StatusPageUID string     `bun:"status_page_uid,notnull"`
	Name          string     `bun:"name,notnull"`
	Slug          string     `bun:"slug,notnull"`
	Position      int        `bun:"position,notnull,default:0"`
	CreatedAt     time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt     time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt     *time.Time `bun:"deleted_at"`
}

// NewStatusPageSection creates a new section with generated UID.
func NewStatusPageSection(pageUID, name, slug string, position int) *StatusPageSection {
	now := time.Now()

	return &StatusPageSection{
		UID:           uuid.New().String(),
		StatusPageUID: pageUID,
		Name:          name,
		Slug:          slug,
		Position:      position,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// StatusPageSectionUpdate represents fields that can be updated on a section.
type StatusPageSectionUpdate struct {
	Name     *string
	Slug     *string
	Position *int
}

// StatusPageResource represents a check assigned to a status page section.
type StatusPageResource struct {
	UID         string    `bun:"uid,pk,type:varchar(36)"`
	SectionUID  string    `bun:"section_uid,notnull"`
	CheckUID    string    `bun:"check_uid,notnull"`
	PublicName  *string   `bun:"public_name"`
	Explanation *string   `bun:"explanation"`
	Position    int       `bun:"position,notnull,default:0"`
	CreatedAt   time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt   time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewStatusPageResource creates a new resource with generated UID.
func NewStatusPageResource(sectionUID, checkUID string, position int) *StatusPageResource {
	now := time.Now()

	return &StatusPageResource{
		UID:        uuid.New().String(),
		SectionUID: sectionUID,
		CheckUID:   checkUID,
		Position:   position,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// StatusPageResourceUpdate represents fields that can be updated on a resource.
type StatusPageResourceUpdate struct {
	PublicName  *string
	Explanation *string
	Position    *int
}
