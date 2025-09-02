// Package regions provides region management for multi-region check execution.
package regions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// ErrRegionMismatch is returned when a worker's region doesn't match any defined region.
var ErrRegionMismatch = errors.New("worker region does not match any defined region")

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

// ValidateWorkerRegion checks that a worker's region matches at least one defined region via prefix matching.
func (s *Service) ValidateWorkerRegion(ctx context.Context, workerRegion string) error {
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

// MatchesRegion returns true if workerRegion starts with jobRegion (prefix matching).
func MatchesRegion(workerRegion, jobRegion string) bool {
	return strings.HasPrefix(workerRegion, jobRegion)
}
