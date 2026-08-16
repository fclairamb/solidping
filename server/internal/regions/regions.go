// Package regions provides region management for multi-region check execution.
package regions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db"
)

// RegionDefinition represents a region where checks can be executed.
type RegionDefinition struct {
	Slug  string `json:"slug"`
	Emoji string `json:"emoji"`
	Name  string `json:"name"`
	// Capabilities carries what the region's live workers say they can do,
	// keyed by capability name (today only CapabilityIPv6). It is a MAP rather
	// than a field so the next capability is additive, and it is omit-empty so
	// a definition round-tripped through the stored `regions` / `custom_regions`
	// parameter stays byte-identical for an older binary that never sets it.
	//
	// This is DERIVED, not configured: it is computed from worker rows at read
	// time and is never persisted by SetOrgCustomRegions.
	Capabilities map[string]string `json:"capabilities,omitempty"`
}

const (
	// ParamRegions is the system parameter key for region definitions.
	ParamRegions = "regions"
	// ParamDefaultRegions is the parameter key for default region slugs.
	ParamDefaultRegions = "default_regions"
	// ParamCustomRegions is the org-scoped parameter key holding an org's
	// private-location (deported-agent) region definitions.
	ParamCustomRegions = "custom_regions"
	// PrivateRegionPrefix is the reserved character that namespaces a private
	// (org-scoped) region. A stored job region for a private location is
	// `@<region-slug>` — ORG-RELATIVE, with no org slug inside it. The character
	// is reserved: cloud/in-process workers may never carry it in their region
	// (ValidateWorkerRegion rejects it, and the workers.region DB check
	// constraint forbids it), which makes it structurally impossible for a cloud
	// worker's prefix match to ever claim a private-region job.
	//
	// The org is deliberately NOT part of the string: every row carrying a
	// region also carries an organization_uid, so an org rename is a pure
	// metadata change instead of a rewrite of five denormalized copies (which is
	// exactly what silently stranded checks before spec 2026-08-13-01). The flip
	// side is that `@aws-paris` is not globally unique, so every path that
	// resolves a private region to rows MUST filter by organization_uid — see
	// wiki/conventions/regions.md for the audit of all of them.
	PrivateRegionPrefix = "@"
	// privateRegionSep separated the org slug from the region slug inside the
	// LEGACY fully-qualified private region string (`@<org>/<slug>`). Kept only
	// to parse legacy input and reject it when it names a foreign org.
	privateRegionSep = "/"
)

var (
	// ErrRegionMismatch is returned when a worker's region doesn't match any defined region.
	ErrRegionMismatch = errors.New("worker region does not match any defined region")
	// ErrPrivateRegionReserved is returned when a cloud/in-process worker is
	// configured with a region that uses the reserved private-region prefix.
	ErrPrivateRegionReserved = errors.New("worker region may not use the reserved private-region prefix '@'")
	// ErrInvalidPrivateRegionSlug is returned when a private region slug is empty
	// or malformed.
	ErrInvalidPrivateRegionSlug = errors.New(
		"private region slug must be 2-30 chars: lowercase letters, digits, and hyphens",
	)
	// ErrForeignPrivateRegion is returned when a caller supplies the legacy
	// fully-qualified `@<org>/<slug>` spelling naming an organization that is
	// NOT the one the request is scoped to. Silently stripping the org would let
	// one org name another org's region, so this fails closed.
	ErrForeignPrivateRegion = errors.New(
		"private region belongs to another organization",
	)
)

// privateRegionSlugRe validates a raw private-region slug (the part after the
// `@<org>/` namespacing). Deliberately narrow so a slug can never contain the
// reserved prefix or separator.
var privateRegionSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,29}$`)

// IsPrivateRegion reports whether a region string is an org-scoped private
// region (namespaced with the reserved prefix).
func IsPrivateRegion(region string) bool {
	return strings.HasPrefix(region, PrivateRegionPrefix)
}

// PrivateRegionSlug builds the stored job-region string for a private location:
// `@<region-slug>`. It takes NO org: the org is implicit in the row's
// organization_uid, and removing the parameter is what stops a future caller
// from reintroducing the rename bug (spec 2026-08-13-01).
func PrivateRegionSlug(regionSlug string) string {
	return PrivateRegionPrefix + regionSlug
}

// ParsePrivateRegion strips the reserved prefix off an org-relative private
// region string, returning the raw region slug. The final return reports
// whether the string was a well-formed org-relative private region; the legacy
// `@<org>/<slug>` spelling is NOT accepted here (see ParseLegacyPrivateRegion).
func ParsePrivateRegion(region string) (string, bool) {
	if !IsPrivateRegion(region) {
		return "", false
	}

	slug := strings.TrimPrefix(region, PrivateRegionPrefix)
	if slug == "" || strings.Contains(slug, privateRegionSep) {
		return "", false
	}

	return slug, true
}

// ParseLegacyPrivateRegion splits the LEGACY fully-qualified private region
// string `@<org>/<slug>` into its org slug and region slug, in that order. It
// exists only so input written against the pre-2026-08-13 API can be
// normalized (when the org matches) or rejected (when it does not); nothing
// ever writes this shape any more.
func ParseLegacyPrivateRegion(region string) (string, string, bool) {
	if !IsPrivateRegion(region) {
		return "", "", false
	}

	rest := strings.TrimPrefix(region, PrivateRegionPrefix)

	org, reg, found := strings.Cut(rest, privateRegionSep)
	if !found || org == "" || reg == "" || strings.Contains(reg, privateRegionSep) {
		return "", "", false
	}

	return org, reg, true
}

// ValidatePrivateRegionSlug checks a raw (un-namespaced) private region slug.
func ValidatePrivateRegionSlug(slug string) error {
	if !privateRegionSlugRe.MatchString(slug) {
		return fmt.Errorf("%w: %q", ErrInvalidPrivateRegionSlug, slug)
	}

	return nil
}

// DefaultRegions returns the default region list used when no regions parameter is configured.
func DefaultRegions() []RegionDefinition {
	return []RegionDefinition{
		{Slug: "default", Emoji: "📍", Name: "Default"},
	}
}

// Service provides region management operations.
type Service struct {
	db db.Service
}

// NewService creates a new regions service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// GetGlobalRegions returns the list of all defined regions from the system parameter.
func (s *Service) GetGlobalRegions(ctx context.Context) ([]RegionDefinition, error) {
	param, err := s.db.GetSystemParameter(ctx, ParamRegions)
	if err != nil {
		return nil, fmt.Errorf("failed to get regions parameter: %w", err)
	}

	if param == nil {
		return DefaultRegions(), nil
	}

	valueBytes, err := json.Marshal(param.Value["value"])
	if err != nil {
		return nil, fmt.Errorf("failed to marshal regions value: %w", err)
	}

	var defs []RegionDefinition
	if err := json.Unmarshal(valueBytes, &defs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal regions: %w", err)
	}

	if len(defs) == 0 {
		return DefaultRegions(), nil
	}

	return defs, nil
}

// GetOrgDefaultRegions returns the default region slugs for an organization.
// Returns nil if no org-level default is configured.
func (s *Service) GetOrgDefaultRegions(ctx context.Context, orgUID string) ([]string, error) {
	param, err := s.db.GetOrgParameter(ctx, orgUID, ParamDefaultRegions)
	if err != nil {
		return nil, fmt.Errorf("failed to get org default_regions parameter: %w", err)
	}

	if param == nil {
		return nil, nil
	}

	valueBytes, err := json.Marshal(param.Value["value"])
	if err != nil {
		return nil, fmt.Errorf("failed to marshal default_regions value: %w", err)
	}

	var slugs []string
	if err := json.Unmarshal(valueBytes, &slugs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal default_regions: %w", err)
	}

	return slugs, nil
}

// getSystemDefaultRegions returns the system-level default region slugs.
func (s *Service) getSystemDefaultRegions(ctx context.Context) ([]string, error) {
	param, err := s.db.GetSystemParameter(ctx, ParamDefaultRegions)
	if err != nil {
		return nil, fmt.Errorf("failed to get system default_regions parameter: %w", err)
	}

	if param == nil {
		return nil, nil
	}

	valueBytes, err := json.Marshal(param.Value["value"])
	if err != nil {
		return nil, fmt.Errorf("failed to marshal system default_regions value: %w", err)
	}

	var slugs []string
	if err := json.Unmarshal(valueBytes, &slugs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system default_regions: %w", err)
	}

	return slugs, nil
}

// ResolveRegionsForCheck determines the effective regions for a check.
// Priority: check regions > org default > system default > all defined regions.
//
// Caller-supplied regions go through NormalizeRegionsForOrg, so the legacy
// fully-qualified `@<org>/<slug>` spelling is accepted for this org (current or
// previous slug) and rejected for anybody else's.
func (s *Service) ResolveRegionsForCheck(ctx context.Context, checkRegions []string, orgUID string) ([]string, error) {
	// 1. If check specifies regions, use those
	if len(checkRegions) > 0 {
		return s.NormalizeRegionsForOrg(ctx, orgUID, checkRegions)
	}

	// 2. Check org default
	orgDefaults, err := s.GetOrgDefaultRegions(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	if len(orgDefaults) > 0 {
		return s.normalizeStoredRegionsForOrg(ctx, orgUID, orgDefaults), nil
	}

	// 3. Check system default
	systemDefaults, err := s.getSystemDefaultRegions(ctx)
	if err != nil {
		return nil, err
	}

	if len(systemDefaults) > 0 {
		return systemDefaults, nil
	}

	// 4. Fall back to all defined region slugs
	defs, err := s.GetGlobalRegions(ctx)
	if err != nil {
		return nil, err
	}

	slugs := make([]string, len(defs))
	for i := range defs {
		slugs[i] = defs[i].Slug
	}

	return slugs, nil
}

// ValidateWorkerRegion checks that a worker's region matches at least one defined
// region via prefix matching. A cloud/in-process worker may NEVER bind a private
// (org-scoped) region: any region carrying the reserved `@` prefix is rejected
// outright, before any prefix matching, so a cloud worker can never be pointed at
// a private location. Private-location execution goes through deported agents,
// which are hard-scoped to their exact bound region and never use this path.
func (s *Service) ValidateWorkerRegion(ctx context.Context, workerRegion string) error {
	if IsPrivateRegion(workerRegion) {
		return fmt.Errorf("%w: %q", ErrPrivateRegionReserved, workerRegion)
	}

	defs, err := s.GetGlobalRegions(ctx)
	if err != nil {
		return fmt.Errorf("failed to load regions for validation: %w", err)
	}

	for i := range defs {
		if MatchesRegion(workerRegion, defs[i].Slug) {
			return nil
		}
	}

	validSlugs := make([]string, len(defs))
	for i := range defs {
		validSlugs[i] = defs[i].Slug
	}

	return fmt.Errorf("%w: %q (valid: %s)",
		ErrRegionMismatch, workerRegion, strings.Join(validSlugs, ", "))
}

// MatchesRegion reports whether a cloud worker in workerRegion may claim a job
// tagged jobRegion. Cloud regions use prefix matching (a worker in "eu-west-1"
// serves jobs tagged "eu"). Private regions (`@<region>`) are a security
// boundary, not a routing convenience: they match ONLY on exact equality, never
// by prefix, so a cloud worker — which can never carry an `@`-prefixed region
// (see ValidateWorkerRegion) — can never prefix-match its way into a private job.
//
// It is deliberately org-BLIND: `@aws-paris` is only unique within an org, so
// this function alone never authorizes anything. Callers pair it with an
// organization_uid predicate (see wiki/conventions/regions.md).
func MatchesRegion(workerRegion, jobRegion string) bool {
	if IsPrivateRegion(jobRegion) || IsPrivateRegion(workerRegion) {
		return workerRegion == jobRegion
	}

	return strings.HasPrefix(workerRegion, jobRegion)
}

// GetOrgCustomRegions returns an org's private-location region definitions
// (raw, un-namespaced slugs). Returns an empty slice when the org has none.
func (s *Service) GetOrgCustomRegions(ctx context.Context, orgUID string) ([]RegionDefinition, error) {
	param, err := s.db.GetOrgParameter(ctx, orgUID, ParamCustomRegions)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom_regions parameter: %w", err)
	}

	if param == nil {
		return nil, nil
	}

	valueBytes, err := json.Marshal(param.Value["value"])
	if err != nil {
		return nil, fmt.Errorf("failed to marshal custom_regions value: %w", err)
	}

	var defs []RegionDefinition
	if err := json.Unmarshal(valueBytes, &defs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal custom_regions: %w", err)
	}

	return defs, nil
}

// SetOrgCustomRegions persists an org's private-location region definitions. Each
// slug is validated; slugs must be unique within the org.
func (s *Service) SetOrgCustomRegions(ctx context.Context, orgUID string, defs []RegionDefinition) error {
	seen := make(map[string]struct{}, len(defs))
	for i := range defs {
		if err := ValidatePrivateRegionSlug(defs[i].Slug); err != nil {
			return err
		}

		if _, dup := seen[defs[i].Slug]; dup {
			return fmt.Errorf("%w: duplicate slug %q", ErrInvalidPrivateRegionSlug, defs[i].Slug)
		}

		seen[defs[i].Slug] = struct{}{}
	}

	// Capabilities are DERIVED from live workers on every read, so they must
	// never be written back into the stored parameter: a persisted copy would
	// be stale the moment a worker's route changed, and would then outrank the
	// truth. Strip whatever a caller happened to pass through.
	stored := make([]RegionDefinition, len(defs))
	for i := range defs {
		stored[i] = defs[i]
		stored[i].Capabilities = nil
	}

	if err := s.db.SetOrgParameter(ctx, orgUID, ParamCustomRegions, stored, false); err != nil {
		return fmt.Errorf("failed to save custom_regions: %w", err)
	}

	return nil
}
