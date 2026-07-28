package models

import (
	"time"

	"github.com/google/uuid"
)

// StatusPagePeriod is the history window a status page renders. It mirrors the
// badge uptime-bar vocabulary: 24h is hourly (24 one-hour buckets), the rest
// are daily (N 24-hour buckets). It is the source of truth for bucketing;
// HistoryDays is kept populated for one release for backward-compat.
type StatusPagePeriod string

// StatusPagePeriod values.
const (
	StatusPagePeriod24h StatusPagePeriod = "24h"
	StatusPagePeriod7d  StatusPagePeriod = "7d"
	StatusPagePeriod30d StatusPagePeriod = "30d"
	StatusPagePeriod90d StatusPagePeriod = "90d"
)

// Valid reports whether the period is one of the four supported values.
func (p StatusPagePeriod) Valid() bool {
	switch p {
	case StatusPagePeriod24h, StatusPagePeriod7d, StatusPagePeriod30d, StatusPagePeriod90d:
		return true
	default:
		return false
	}
}

// IsHourly reports whether the period buckets by hour (only 24h does).
func (p StatusPagePeriod) IsHourly() bool {
	return p == StatusPagePeriod24h
}

// PeriodFromDays maps a legacy history_days count to the closest period enum,
// used to backfill rows and to accept the deprecated historyDays input for one
// release. 7→7d, 30→30d, everything else (including 90) →90d.
func PeriodFromDays(days int) StatusPagePeriod {
	switch days {
	case 7:
		return StatusPagePeriod7d
	case 30:
		return StatusPagePeriod30d
	default:
		return StatusPagePeriod90d
	}
}

// StatusPage represents a public status page for an organization.
type StatusPage struct {
	UID              string  `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID  string  `bun:"organization_uid,notnull"`
	Name             string  `bun:"name,notnull"`
	Slug             string  `bun:"slug,notnull"`
	Description      *string `bun:"description"`
	Visibility       string  `bun:"visibility,notnull,default:'public'"`
	IsDefault        bool    `bun:"is_default,notnull,default:false"`
	Enabled          bool    `bun:"enabled,notnull,default:true"`
	ShowAvailability bool    `bun:"show_availability,notnull,default:true"`
	ShowResponseTime bool    `bun:"show_response_time,notnull,default:true"`
	HistoryDays      int     `bun:"history_days,notnull,default:90"`
	HistoryPeriod    string  `bun:"history_period,notnull,default:'90d'"`
	Language         *string `bun:"language"`
	// CustomDomain is a customer-owned hostname (punycode/ASCII, lowercased)
	// the page is served on. nil = none. Globally unique among live rows.
	CustomDomain *string `bun:"custom_domain"`
	// CustomDomainToken is the opaque base64url DNS-challenge token, set while a
	// domain is configured. Never exposed on public endpoints.
	CustomDomainToken *string `bun:"custom_domain_token"`
	// CustomDomainVerifiedAt is when the domain last passed verification
	// (TXT + CNAME). nil = unverified — only verified pages are served on the
	// custom host.
	CustomDomainVerifiedAt *time.Time `bun:"custom_domain_verified_at"`
	// CustomDomainCheckedAt is when the periodic re-verification job last
	// checked this domain.
	CustomDomainCheckedAt *time.Time `bun:"custom_domain_checked_at"`
	// CustomDomainFailures counts consecutive re-verification failures. At 3 the
	// verification is cleared (domain release/takeover protection).
	CustomDomainFailures int        `bun:"custom_domain_failures,notnull,default:0"`
	CreatedAt            time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt            time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt            *time.Time `bun:"deleted_at"`
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
		HistoryPeriod:    string(StatusPagePeriod90d),
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
	HistoryPeriod    *string
	Language         *string
}

// StatusPageCustomDomainUpdate is the whole-lifecycle writer for a status
// page's custom-domain columns. Every field is written verbatim (a full
// overwrite of all five columns plus updated_at), so it expresses every
// transition — set, clear, verify-now, and the periodic re-verify job — in one
// shape. Domain/Token nil clears the columns to NULL; VerifiedAt/CheckedAt nil
// clears those timestamps.
type StatusPageCustomDomainUpdate struct {
	Domain     *string
	Token      *string
	VerifiedAt *time.Time
	CheckedAt  *time.Time
	Failures   int
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
