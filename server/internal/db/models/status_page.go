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

// Status page visibility values. `public` is world-readable, `private` is
// fully hidden (the public endpoints 404 as if it did not exist), and
// `password` is shared-with-a-secret: the public endpoints answer 401 until
// the visitor unlocks the page (spec 2026-08-21-07).
const (
	StatusPageVisibilityPublic   = "public"
	StatusPageVisibilityPrivate  = "private"
	StatusPageVisibilityPassword = "password"
)

// ValidStatusPageVisibility reports whether v is one of the three supported
// visibility values.
func ValidStatusPageVisibility(v string) bool {
	switch v {
	case StatusPageVisibilityPublic, StatusPageVisibilityPrivate, StatusPageVisibilityPassword:
		return true
	default:
		return false
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
	// AutoPublish turns the incident auto-publication pipeline on for this
	// page. The DDL default is FALSE so that upgrading an existing
	// installation never makes yesterday's internal blips public; NEW pages
	// opt in through NewStatusPage instead (spec 2026-08-19-08).
	AutoPublish bool `bun:"auto_publish,notnull,default:false"`
	// AutoPublishDelaySeconds debounces publication: an incident must still be
	// open this long after it opened before customers hear about it.
	//
	// 0 is legal and means "publish immediately", which is why the bun tag
	// carries NO `default:` clause even though the column has one. bun omits a
	// zero-valued field from an INSERT when the tag declares a default, so
	// `default:60` here would silently turn an operator's deliberate "publish
	// immediately" into a one-minute delay — the value would never reach the
	// database at all. The DDL default still applies to rows inserted outside
	// the application (an upgraded installation's existing pages).
	AutoPublishDelaySeconds int `bun:"auto_publish_delay_seconds,notnull"`
	// AutoResolve decides what an auto-created publication does when its
	// incident resolves: always | if_untouched | never.
	AutoResolve string `bun:"auto_resolve,notnull,default:'if_untouched'"`
	// CustomCSS is operator-authored CSS injected into the public status page
	// as a <style> text node (never dangerouslySetInnerHTML). nil = none.
	// Capped at 64 KB and @import-free by API validation; unlike the
	// custom-domain columns it IS exposed on public responses, since the
	// public renderer is its only consumer.
	CustomCSS *string `bun:"custom_css"`
	// CustomDomain is a customer-owned hostname (punycode/ASCII, lowercased)
	// the page is served on. nil = none. Globally unique among live rows.
	CustomDomain *string `bun:"custom_domain"`
	// CustomDomainToken is the opaque, DNS-label-safe token (lowercase base32),
	// set while a domain is configured; in token mode it is the leading label of
	// the expected CNAME target. Never exposed on public endpoints.
	CustomDomainToken *string `bun:"custom_domain_token"`
	// CustomDomainVerifiedAt is when the domain last passed CNAME verification.
	// nil = unverified — only verified pages are served on the custom host.
	CustomDomainVerifiedAt *time.Time `bun:"custom_domain_verified_at"`
	// CustomDomainCheckedAt is when the periodic re-verification job last
	// checked this domain.
	CustomDomainCheckedAt *time.Time `bun:"custom_domain_checked_at"`
	// CustomDomainFailures counts consecutive re-verification failures. At 3 the
	// verification is cleared (domain release/takeover protection).
	CustomDomainFailures int `bun:"custom_domain_failures,notnull,default:0"`
	// LogoFileUID / FaviconFileUID are the `files.uid` of the page's own brand
	// assets (spec 2026-08-21-07). nil = wear the SolidPing logo / the default
	// favicon. They hold a file UID rather than a URL because the public route
	// that serves them is unsigned and authorized purely by state: a blob is
	// public exactly while it is the CURRENT asset of a live, enabled page.
	LogoFileUID    *string `bun:"logo_file_uid"`
	FaviconFileUID *string `bun:"favicon_file_uid"`
	// HideBranding is the page's half of the white-label decision: the
	// "powered by SolidPing" footer disappears only when this is true AND the
	// org holds the `whiteLabel` entitlement. Keeping the two halves separate
	// means a downgrade silently restores the badge without rewriting the page.
	HideBranding bool `bun:"hide_branding,notnull,default:false"`
	// PasswordHash is the password hash gating a `visibility = password` page.
	// It is produced by internal/utils/passwords, whose active policy is
	// argon2id by default (bcrypt is selectable) — the same policy user
	// passwords use, so there is one hashing decision in the system, not two.
	// It is NEVER serialized onto any response — reads expose `hasPassword`
	// only. nil on public/private pages.
	PasswordHash *string `bun:"password_hash"`
	// Settings holds per-page display customization (e.g. availability color
	// thresholds), typed rather than a free-form map so keys stay
	// discoverable (spec 2026-08-03-01). Column is NOT NULL DEFAULT '{}'.
	Settings  StatusPageSettings `bun:"settings,type:jsonb,notnull"`
	CreatedAt time.Time          `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time          `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt *time.Time         `bun:"deleted_at"`
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
		// Auto-publish is ON for pages created from here on. The migration
		// deliberately leaves existing rows at false — see the column comment.
		AutoPublish:             true,
		AutoPublishDelaySeconds: DefaultAutoPublishDelaySeconds,
		AutoResolve:             string(AutoResolveIfUntouched),
		CreatedAt:               now,
		UpdatedAt:               now,
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
	// AutoPublish / AutoPublishDelaySeconds / AutoResolve are the incident
	// auto-publication settings (spec 2026-08-19-08). nil leaves untouched.
	AutoPublish             *bool
	AutoPublishDelaySeconds *int
	AutoResolve             *string
	// CustomCSS updates the page's custom stylesheet. A pointer to the empty
	// string clears the column (the appearance editor's "empty textarea"), a
	// nil pointer leaves it untouched.
	CustomCSS *string
	// HideBranding flips the page-level white-label opt-in. nil leaves it.
	HideBranding *bool
	// PasswordHash writes the password hash gating a password page. A pointer to
	// the empty string CLEARS the column (the page stopped being password
	// protected); a nil pointer leaves it untouched, which is what keeps an
	// unrelated PATCH from silently unlocking a page.
	PasswordHash *string
	// Settings overwrites the whole settings column when non-nil (the caller
	// — statuspages.Service — has already applied the no-deep-merge
	// section-replace-or-reset semantics against the current value). A nil
	// pointer leaves the column untouched.
	Settings *StatusPageSettings
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

// StatusPageBrandingUpdate is the whole-column writer for a status page's two
// brand-asset columns. Both fields are written verbatim on every call (nil
// clears the column to NULL), mirroring StatusPageCustomDomainUpdate: one
// shape expresses set, replace and clear, so no caller can accidentally leave
// a stale file UID behind and keep a retired blob publicly reachable.
type StatusPageBrandingUpdate struct {
	LogoFileUID    *string
	FaviconFileUID *string
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

// StatusPageResource represents a check OR a check group assigned to a status
// page section (spec 2026-08-01-03). Exactly one of CheckUID / CheckGroupUID is
// set — the database enforces it with an XOR check constraint, mirroring
// MaintenanceWindowCheck.
//
// A group resource renders as ONE public component: rolled-up status, weighted
// average availability across members, and maintenance from a group- or
// member-targeted window. Members are never listed publicly.
type StatusPageResource struct {
	UID        string `bun:"uid,pk,type:varchar(36)"`
	SectionUID string `bun:"section_uid,notnull"`
	// CheckUID is the individual check to display. nil when the resource
	// targets a group.
	CheckUID *string `bun:"check_uid"`
	// CheckGroupUID is the check group to display as one aggregated component.
	// nil when the resource targets an individual check.
	CheckGroupUID *string `bun:"check_group_uid"`
	PublicName    *string `bun:"public_name"`
	Explanation   *string `bun:"explanation"`
	// AutoPublish overrides the page-level auto-publish setting for this one
	// resource. nil (the default) means "inherit the page" — it is a
	// three-state column on purpose, so a page can be flipped on or off
	// without silently rewriting every resource's intent.
	AutoPublish *bool     `bun:"auto_publish"`
	Position    int       `bun:"position,notnull,default:0"`
	CreatedAt   time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt   time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// IsGroup reports whether the resource targets a check group rather than an
// individual check.
func (r *StatusPageResource) IsGroup() bool {
	return r.CheckGroupUID != nil && *r.CheckGroupUID != ""
}

// NewStatusPageResource creates a new check-targeting resource with generated UID.
func NewStatusPageResource(sectionUID, checkUID string, position int) *StatusPageResource {
	now := time.Now()

	return &StatusPageResource{
		UID:        uuid.New().String(),
		SectionUID: sectionUID,
		CheckUID:   &checkUID,
		Position:   position,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// NewStatusPageGroupResource creates a new group-targeting resource with
// generated UID.
func NewStatusPageGroupResource(sectionUID, checkGroupUID string, position int) *StatusPageResource {
	now := time.Now()

	return &StatusPageResource{
		UID:           uuid.New().String(),
		SectionUID:    sectionUID,
		CheckGroupUID: &checkGroupUID,
		Position:      position,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// StatusPageResourceUpdate represents fields that can be updated on a resource.
type StatusPageResourceUpdate struct {
	PublicName  *string
	Explanation *string
	Position    *int
	// SetAutoPublish must be true for AutoPublish to be written at all; that
	// is what lets a caller reset the override back to "inherit" (SetAutoPublish
	// true, AutoPublish nil) as distinct from "leave alone".
	SetAutoPublish bool
	AutoPublish    *bool
	// SetTarget switches the resource's target kind. When true, BOTH target
	// columns are written: exactly one of CheckUID / CheckGroupUID must be
	// non-nil and the other column is set to NULL, so the XOR constraint always
	// holds. When false the target is left untouched.
	SetTarget     bool
	CheckUID      *string
	CheckGroupUID *string
}
