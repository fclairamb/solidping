// Package regions provides HTTP handlers for region API endpoints.
package regions

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Service provides business logic for region management.
type Service struct {
	db      db.Service
	regions *regions.Service
}

// NewService creates a new regions handler service.
func NewService(dbService db.Service) *Service {
	return &Service{
		db:      dbService,
		regions: regions.NewService(dbService),
	}
}

// RegionResponse represents a region in API responses. For a private (org-scoped)
// region the Slug is the org-relative `@<slug>` string actually stored on checks
// — the picker can therefore treat every entry uniformly — and Private is true
// so the UI can badge it as a customer-run location.
type RegionResponse struct {
	Slug  string `json:"slug"`
	Emoji string `json:"emoji"`
	Name  string `json:"name"`
	// Private marks an org-private region served by deported agents.
	Private bool `json:"private,omitempty"`
	// Capabilities reports what the region's LIVE workers say they can do,
	// today only `ipv6` with a three-state value ("yes" / "no" / "unknown").
	// Additive and ignorable: a client that does not read it behaves exactly as
	// it did before the field existed, and "unknown" must never be rendered as
	// "no" (spec 2026-08-15-11).
	Capabilities map[string]string `json:"capabilities,omitempty"`
}

// ListGlobalRegionsResponse is the response for listing global regions.
type ListGlobalRegionsResponse struct {
	Data []RegionResponse `json:"data"`
}

// ListOrgRegionsResponse is the response for listing org regions.
type ListOrgRegionsResponse struct {
	Data           []RegionResponse `json:"data"`
	DefaultRegions []string         `json:"defaultRegions"`
}

// ListGlobalRegions returns all globally defined regions.
func (s *Service) ListGlobalRegions(ctx context.Context) (*ListGlobalRegionsResponse, error) {
	defs, err := s.regions.GetGlobalRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get global regions: %w", err)
	}

	if capErr := s.regions.AnnotateGlobalCapabilities(ctx, defs); capErr != nil {
		return nil, fmt.Errorf("failed to compute region capabilities: %w", capErr)
	}

	data := make([]RegionResponse, len(defs))
	for i := range defs {
		data[i] = RegionResponse{
			Slug:         defs[i].Slug,
			Emoji:        defs[i].Emoji,
			Name:         defs[i].Name,
			Capabilities: defs[i].Capabilities,
		}
	}

	return &ListGlobalRegionsResponse{Data: data}, nil
}

// ListOrgRegions returns regions available to an organization along with default regions.
func (s *Service) ListOrgRegions(ctx context.Context, orgSlug string) (*ListOrgRegionsResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	// Get global region definitions
	defs, err := s.regions.GetGlobalRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get global regions: %w", err)
	}

	if capErr := s.regions.AnnotateGlobalCapabilities(ctx, defs); capErr != nil {
		return nil, fmt.Errorf("failed to compute region capabilities: %w", capErr)
	}

	data := make([]RegionResponse, 0, len(defs))
	for i := range defs {
		data = append(data, RegionResponse{
			Slug:         defs[i].Slug,
			Emoji:        defs[i].Emoji,
			Name:         defs[i].Name,
			Capabilities: defs[i].Capabilities,
		})
	}

	// Append the org's private locations (spec 2026-07-16-02) so the check-form
	// region picker offers them alongside cloud regions. They are exposed under
	// their org-relative `@<slug>` identity — the exact string stored on the
	// check and matched by the agent — so a caller never has to namespace it, and
	// renaming the org never changes the string (spec 2026-08-13-01).
	privateDefs, err := s.regions.GetOrgCustomRegions(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to get org private regions: %w", err)
	}

	// Private capability is resolved through this org's agents only — see
	// AnnotatePrivateCapabilities for why matching on the region string alone
	// would pool two orgs' identically-named locations.
	if capErr := s.regions.AnnotatePrivateCapabilities(ctx, org.UID, privateDefs); capErr != nil {
		return nil, fmt.Errorf("failed to compute private region capabilities: %w", capErr)
	}

	for i := range privateDefs {
		data = append(data, RegionResponse{
			Slug:         regions.PrivateRegionSlug(privateDefs[i].Slug),
			Emoji:        privateDefs[i].Emoji,
			Name:         privateDefs[i].Name,
			Private:      true,
			Capabilities: privateDefs[i].Capabilities,
		})
	}

	// Get default regions (resolves cascade: org > system > all)
	defaultRegions, err := s.regions.ResolveRegionsForCheck(ctx, nil, org.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve default regions: %w", err)
	}

	return &ListOrgRegionsResponse{
		Data:           data,
		DefaultRegions: defaultRegions,
	}, nil
}
