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
	// `@<org-slug>/<region-slug>`. The character is reserved: cloud/in-process
	// workers may never carry it in their region (ValidateWorkerRegion rejects
	// it, and the workers.region DB check constraint forbids it), which makes it
	// structurally impossible for a cloud worker's prefix match to ever claim a
	// private-region job.
	PrivateRegionPrefix = "@"
	// privateRegionSep separates the org slug from the region slug inside a
	// fully-qualified private region string.
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

// PrivateRegionSlug builds the fully-qualified stored job-region string for a
// private location: `@<org-slug>/<region-slug>`.
func PrivateRegionSlug(orgSlug, regionSlug string) string {
	return PrivateRegionPrefix + orgSlug + privateRegionSep + regionSlug
}

// ParsePrivateRegion splits a fully-qualified private region string back into
// its org slug and region slug, in that order. The final return reports whether
// the string was a well-formed private region.
func ParsePrivateRegion(region string) (string, string, bool) {
	if !IsPrivateRegion(region) {
		return "", "", false
	}

	rest := strings.TrimPrefix(region, PrivateRegionPrefix)

	org, reg, found := strings.Cut(rest, privateRegionSep)
	if !found || org == "" || reg == "" {
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
func (s *Service) ResolveRegionsForCheck(ctx context.Context, checkRegions []string, orgUID string) ([]string, error) {
	// 1. If check specifies regions, use those
	if len(checkRegions) > 0 {
		return checkRegions, nil
	}

	// 2. Check org default
	orgDefaults, err := s.GetOrgDefaultRegions(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	if len(orgDefaults) > 0 {
		return orgDefaults, nil
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
// serves jobs tagged "eu"). Private regions (`@<org>/<region>`) are a security
// boundary, not a routing convenience: they match ONLY on exact equality, never
// by prefix, so a cloud worker — which can never carry an `@`-prefixed region
// (see ValidateWorkerRegion) — can never prefix-match its way into a private job.
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

	if err := s.db.SetOrgParameter(ctx, orgUID, ParamCustomRegions, defs, false); err != nil {
		return fmt.Errorf("failed to save custom_regions: %w", err)
	}

	return nil
}
