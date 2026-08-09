package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Organization represents a tenant in the system.
type Organization struct {
	UID  string `bun:"uid,pk,type:varchar(36)"`
	Slug string `bun:"slug,notnull"`
	Name string `bun:"name"`
	// DefaultEscalationPolicyUID is the org-wide fallback escalation policy for
	// checks that resolve to no policy of their own (check → group → org default
	// → none). NULL = no org default (legacy behavior). FK `on delete set null`.
	DefaultEscalationPolicyUID *string `bun:"default_escalation_policy_uid"`
	// LogoURL is the organization's logo: either an external http(s) URL, or
	// "/pub/org-logos/<file uid>" for a logo uploaded through the files
	// subsystem. NULL means "no logo", and every surface falls back to the
	// product default.
	LogoURL *string `bun:"logo_url"`
	// LogoFileUID points at the uploaded file backing LogoURL. It is NULL for
	// external-URL logos. Two things depend on it: replacing an uploaded logo
	// retires the previous blob, and the unsigned public logo route only
	// serves a file that is the CURRENT logo of a live organization.
	LogoFileUID *string    `bun:"logo_file_uid"`
	CreatedAt   time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt   time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt   *time.Time `bun:"deleted_at"`
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
	// DefaultEscalationPolicyUID sets the org-wide default escalation policy.
	// ClearDefaultEscalationPolicyUID takes precedence and clears it to NULL
	// (mirrors the check-group clear-flag pattern).
	DefaultEscalationPolicyUID      *string
	ClearDefaultEscalationPolicyUID bool
	// LogoURL / LogoFileUID follow the same set-or-clear pattern: the Clear*
	// flag wins and writes NULL, a non-nil pointer writes the value, and nil
	// with no flag leaves the column untouched (PATCH semantics).
	LogoURL          *string
	ClearLogoURL     bool
	LogoFileUID      *string
	ClearLogoFileUID bool
}

// OrganizationPreviousSlug is a slug an organization used to answer on before
// it was renamed. Resolution always prefers a live organizations.slug, so an
// alias can never shadow an existing org; on a miss the alias resolves and the
// request is redirected to the organization's current slug.
//
// The alias is released (soft-deleted) as soon as another organization claims
// the slug, which is the only guarantee boundary the UI promises. Deleted orgs
// are deliberately unreachable through aliases (spec 2026-08-08-11): the
// lookup joins organizations and requires deleted_at IS NULL.
type OrganizationPreviousSlug struct {
	bun.BaseModel `bun:"table:organization_previous_slugs,alias:org_previous_slug"`

	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Slug            string     `bun:"slug,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewOrganizationPreviousSlug creates an alias row with a generated UID.
func NewOrganizationPreviousSlug(orgUID, slug string) *OrganizationPreviousSlug {
	return &OrganizationPreviousSlug{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Slug:            slug,
		CreatedAt:       time.Now(),
	}
}
