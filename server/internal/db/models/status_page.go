package models

import (
	"encoding/json"
	"fmt"
	"strconv"
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
	// CustomDomainFailures counts consecutive re-verification failures. At
	// CustomDomainGraceAfterFailures the domain enters `grace` (still served);
	// only at CustomDomainHardDemoteAfterFailures is the verification cleared
	// (domain release/takeover protection).
	CustomDomainFailures int `bun:"custom_domain_failures,notnull,default:0"`
	// CustomDomainState is the explicit lifecycle state — one of the
	// CustomDomainState* constants. It exists because a single failure counter
	// was doing two jobs ("flaky right now" and "gone for good"), which made a
	// DNS blip indistinguishable from a domain transfer and took status pages
	// dark permanently (spec 2026-08-23-03).
	CustomDomainState string `bun:"custom_domain_state,notnull,default:'none'"`
	// CustomDomainSuccesses counts consecutive SUCCESSFUL re-verifications. It
	// is the counter re-promotion is earned with: a demoted domain needs
	// CustomDomainRepromoteSuccesses of them in a row (plus a still-valid
	// certificate) before the sweep trusts it again.
	CustomDomainSuccesses int `bun:"custom_domain_successes,notnull,default:0"`
	// CustomDomainGraceSince is when the domain last entered `grace`. nil
	// outside grace. Makes "how long has this been degrading" readable instead
	// of inferred from a counter times a job interval.
	CustomDomainGraceSince *time.Time `bun:"custom_domain_grace_since"`
	// CustomDomainLastCheck is a human-readable diagnostic from the last
	// re-verification: the mode used, the expected CNAME target, what DNS
	// actually returned, and the lookup error if any. Without it, "verification
	// fails but dig says the record is right" is only investigable by
	// correlating server logs with manual dig runs.
	CustomDomainLastCheck *string `bun:"custom_domain_last_check"`
	// PasswordHash is the password hash gating a `visibility = password` page.
	// It is produced by internal/utils/passwords, whose active policy is
	// argon2id by default (bcrypt is selectable) — the same policy user
	// passwords use, so there is one hashing decision in the system, not two.
	// It is NEVER serialized onto any response — reads expose `hasPassword`
	// only. nil on public/private pages.
	PasswordHash *string `bun:"password_hash"`
	// Settings holds per-page display customization — availability color
	// thresholds and the page's brand identity (logo, favicon, white-label
	// opt-in) — typed rather than a free-form map so keys stay discoverable
	// (specs 2026-08-03-01, 2026-08-22-03). Column is NOT NULL DEFAULT '{}'.
	//
	// Read the branding keys through the Settings accessors
	// (Settings.LogoFileUID(), .FaviconFileUID(), .HideBranding()) rather than
	// reaching into .Branding, which is nil on a page that never set one.
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
	// HideBranding flips the page-level white-label opt-in, stored in
	// `settings -> branding -> hideBranding`. nil leaves it. The DB layer
	// merges it into the JSON rather than overwriting the column — and folds
	// it into Settings when that is set in the same call, since two
	// `SET settings = ...` clauses in one UPDATE is a Postgres error.
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
	// State is the lifecycle state to write. The zero value is deliberately
	// NOT a legal state: every caller states its intent, and the DB layer
	// normalizes "" to CustomDomainStateNone so a forgotten field clears the
	// domain rather than silently keeping a stale `active`.
	State string
	// Successes is the consecutive-success counter (see the model field).
	Successes int
	// GraceSince is when the domain entered grace; nil clears it.
	GraceSince *time.Time
	// LastCheck is the diagnostic string from the last re-verification; nil
	// clears it.
	LastCheck *string
}

// Custom-domain lifecycle states. Stored in status_pages.custom_domain_state.
//
// The states are ordered by trust: none -> pending -> active, with grace as the
// "still serving but the re-checks are failing" holding pen and demoted as the
// terminal-until-recovered state. ONLY `demoted` has
// custom_domain_verified_at = NULL; `grace` keeps it set, and that is precisely
// what makes a DNS blip invisible to the page's visitors.
const (
	// CustomDomainStateNone means no custom domain is configured.
	CustomDomainStateNone = "none"
	// CustomDomainStatePending means a domain is configured but has never
	// verified. The sweep NEVER auto-promotes out of this state — a first
	// verification is an operator action, so a hostname someone else parked a
	// CNAME on cannot bootstrap itself into being served.
	CustomDomainStatePending = "pending"
	// CustomDomainStateActive means verified and serving.
	CustomDomainStateActive = "active"
	// CustomDomainStateGrace means still verified and STILL SERVING, but
	// re-verification is currently failing. The common case (a transient DNS
	// fault) lives and dies here without a visitor ever noticing.
	CustomDomainStateGrace = "grace"
	// CustomDomainStateDemoted means the domain stayed unreachable well past
	// the grace window and verification was cleared. Recoverable: the sweep
	// re-promotes it after CustomDomainRepromoteSuccesses consecutive
	// successes while a valid certificate is still held.
	CustomDomainStateDemoted = "demoted"
)

// ValidCustomDomainState reports whether s is one of the lifecycle states.
func ValidCustomDomainState(s string) bool {
	switch s {
	case CustomDomainStateNone, CustomDomainStatePending, CustomDomainStateActive,
		CustomDomainStateGrace, CustomDomainStateDemoted:
		return true
	default:
		return false
	}
}

// StatusPageBrandingUpdate is the whole-SECTION writer for a status page's
// branding: it replaces `settings -> branding` in full on every call, so one
// shape expresses set, replace and clear and no caller can accidentally leave
// a stale file UID behind and keep a retired blob publicly reachable.
//
// "In full" stops at the branding section. The write is a two-level JSON merge
// in SQL (see UpdateStatusPageBranding in each dialect), never a
// read-modify-write of the whole settings column in Go — that would clobber a
// concurrent `availability` change, which is the single most likely regression
// this storage move could introduce.
type StatusPageBrandingUpdate struct {
	LogoFileUID    *string
	FaviconFileUID *string
	HideBranding   bool
}

// brandingPatch is the JSON merge patch a StatusPageBrandingUpdate becomes.
//
// It exists instead of reusing BrandingSettings because BrandingSettings has
// `omitempty` on the file UIDs: a cleared slot would be OMITTED from the patch
// and the previous value would survive the merge — a retired blob staying
// publicly reachable, which is exactly the failure the whole-section write
// shape exists to prevent. Here a nil pointer travels as an explicit JSON
// null, which Postgres stores as null and SQLite's json_patch removes; both
// decode back to a nil *string.
type brandingPatch struct {
	LogoFileUID    *string `json:"logoFileUid"`
	FaviconFileUID *string `json:"faviconFileUid"`
	HideBranding   bool    `json:"hideBranding"`
}

// settingsPatch wraps a branding patch at the `settings` level, which is the
// shape SQLite's json_patch (RFC 7386, recursive) takes directly.
type settingsPatch struct {
	Branding brandingPatch `json:"branding"`
}

// BrandingPatchJSON returns the merge patch for the `branding` SECTION alone —
// what Postgres concatenates onto `settings->'branding'`.
func (u *StatusPageBrandingUpdate) BrandingPatchJSON() (string, error) {
	data, err := json.Marshal(u.patch())
	if err != nil {
		return "", fmt.Errorf("failed to marshal branding patch: %w", err)
	}

	return string(data), nil
}

// SettingsPatchJSON returns the merge patch rooted at `settings` — what SQLite
// hands to json_patch.
func (u *StatusPageBrandingUpdate) SettingsPatchJSON() (string, error) {
	data, err := json.Marshal(settingsPatch{Branding: u.patch()})
	if err != nil {
		return "", fmt.Errorf("failed to marshal branding patch: %w", err)
	}

	return string(data), nil
}

func (u *StatusPageBrandingUpdate) patch() brandingPatch {
	return brandingPatch{
		LogoFileUID:    u.LogoFileUID,
		FaviconFileUID: u.FaviconFileUID,
		HideBranding:   u.HideBranding,
	}
}

// HideBrandingSectionPatch returns the `branding`-rooted merge patch that flips
// ONLY the white-label opt-in, leaving the two asset keys alone — what Postgres
// concatenates onto `settings->'branding'`. It is the generic PATCH path's
// write (StatusPageUpdate.HideBranding).
func HideBrandingSectionPatch(hide bool) string {
	return `{"hideBranding":` + strconv.FormatBool(hide) + `}`
}

// HideBrandingSettingsPatch returns the same patch rooted at `settings` — what
// SQLite hands to json_patch.
func HideBrandingSettingsPatch(hide bool) string {
	return `{"branding":` + HideBrandingSectionPatch(hide) + `}`
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
