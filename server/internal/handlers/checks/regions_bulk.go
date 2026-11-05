package checks

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Reasons a check is left out of the bulk "set regions" action.
const (
	SetRegionsSkipNotFound = "not_found"
	SetRegionsSkipPassive  = "passive"
	SetRegionsSkipReadOnly = "read_only"
	SetRegionsSkipRejected = "rejected"
)

var (
	errSetRegionsNoChecks = errors.New("checkUids: at least one check is required")
	errSetRegionsNoRegion = errors.New("regions: at least one region is required")
	errSetRegionsUnknown  = errors.New("regions: unknown region")
)

// IsSetRegionsRequestError reports whether err is a rejection of the bulk
// request itself (a 400), as opposed to a per-check skip.
func IsSetRegionsRequestError(err error) bool {
	return errors.Is(err, errSetRegionsNoChecks) || errors.Is(err, errSetRegionsNoRegion) ||
		errors.Is(err, errSetRegionsUnknown) || errors.Is(err, regions.ErrForeignPrivateRegion) ||
		errors.Is(err, regions.ErrInvalidPrivateRegionSlug)
}

// SetRegionsRequest is the body of POST /api/v1/orgs/:org/checks/regions.
type SetRegionsRequest struct {
	// CheckUIDs are the checks to pin.
	CheckUIDs []string `json:"checkUids"`
	// Regions is the exact region set every listed check is pinned to.
	Regions []string `json:"regions"`
	// DryRun reports what would be updated without writing anything.
	DryRun bool `json:"dryRun,omitempty"`
}

// SetRegionsItem is one check updated (or that would be).
type SetRegionsItem struct {
	UID     string   `json:"uid"`
	Slug    *string  `json:"slug,omitempty"`
	Name    *string  `json:"name,omitempty"`
	Regions []string `json:"regions"`
}

// SetRegionsSkip is one requested check left untouched, and why.
type SetRegionsSkip struct {
	UID     string  `json:"uid"`
	Slug    *string `json:"slug,omitempty"`
	Reason  string  `json:"reason"`
	Message string  `json:"message,omitempty"`
}

// SetRegionsResponse is the answer of the bulk pin.
type SetRegionsResponse struct {
	Updated  []SetRegionsItem            `json:"updated"`
	Skipped  []SetRegionsSkip            `json:"skipped"`
	Warnings []base.ValidationErrorField `json:"warnings,omitempty"`
	DryRun   bool                        `json:"dryRun"`
}

// SetRegions pins every listed check to exactly req.Regions, with the
// semantics of an explicit `regions` list on a single-check PATCH (spec
// 2026-11-05-01). It is per-check, like the bulk switch to automatic
// placement: a check that cannot take the regions is skipped with its reason
// and the others are still updated. Checks of other organizations are
// reported as not_found.
func (s *Service) SetRegions(
	ctx context.Context, orgSlug string, req *SetRegionsRequest,
) (*SetRegionsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	if len(req.CheckUIDs) == 0 {
		return nil, errSetRegionsNoChecks
	}

	if len(req.Regions) == 0 {
		return nil, errSetRegionsNoRegion
	}

	resolved, err := s.validateBulkRegions(ctx, org.UID, req.Regions)
	if err != nil {
		return nil, err
	}

	var rows []*models.Check

	if err := s.db.DB().NewSelect().
		Model(&rows).
		Where("organization_uid = ?", org.UID).
		Where("deleted_at IS NULL").
		Where("uid IN (?)", bun.List(req.CheckUIDs)).
		Order("created_at", "uid").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list checks to set regions: %w", err)
	}

	resp := &SetRegionsResponse{Updated: []SetRegionsItem{}, Skipped: []SetRegionsSkip{}, DryRun: req.DryRun}
	found := make(map[string]bool, len(rows))

	for _, check := range rows {
		found[check.UID] = true

		skip, warnings := s.setRegionsOne(ctx, orgSlug, check, resolved, req.DryRun)
		if skip != nil {
			resp.Skipped = append(resp.Skipped, *skip)

			continue
		}

		resp.Warnings = mergeWarnings(resp.Warnings, warnings)
		resp.Updated = append(resp.Updated, SetRegionsItem{
			UID: check.UID, Slug: check.Slug, Name: check.Name, Regions: slices.Clone(resolved),
		})
	}

	for _, uid := range req.CheckUIDs {
		if !found[uid] {
			resp.Skipped = append(resp.Skipped, SetRegionsSkip{UID: uid, Reason: SetRegionsSkipNotFound})
			found[uid] = true
		}
	}

	return resp, nil
}

// validateBulkRegions checks the requested regions once for the whole batch:
// each must be a declared cloud region or a private (@) location of the org.
func (s *Service) validateBulkRegions(ctx context.Context, orgUID string, requested []string) ([]string, error) {
	resolved, err := s.regions.ResolveRegionsForCheck(ctx, requested, orgUID)
	if err != nil {
		return nil, err
	}

	declared, err := s.regions.GetGlobalRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list declared regions: %w", err)
	}

	for _, slug := range resolved {
		if regions.IsPrivateRegion(slug) {
			continue
		}

		if !slices.ContainsFunc(declared, func(def regions.RegionDefinition) bool { return def.Slug == slug }) {
			return nil, fmt.Errorf("%w: %q", errSetRegionsUnknown, slug)
		}
	}

	return resolved, nil
}

// setRegionsOne applies (or, on a dry run, only validates) the pin for one
// check. A nil skip means the check is updated.
func (s *Service) setRegionsOne(
	ctx context.Context, orgSlug string, check *models.Check, resolved []string, dryRun bool,
) (*SetRegionsSkip, []base.ValidationErrorField) {
	skip := func(reason string, err error) (*SetRegionsSkip, []base.ValidationErrorField) {
		out := &SetRegionsSkip{UID: check.UID, Slug: check.Slug, Reason: reason}
		if err != nil {
			out.Message = err.Error()
		}

		return out, nil
	}

	switch {
	case check.IsPassive():
		return skip(SetRegionsSkipPassive, nil)
	case assertDemoMayWriteCheck(ctx, check) != nil:
		return skip(SetRegionsSkipReadOnly, nil)
	}

	pinned := slices.Clone(resolved)

	if dryRun {
		if err := s.dryRunPin(ctx, check, pinned); err != nil {
			return skip(SetRegionsSkipRejected, err)
		}

		return nil, nil
	}

	updated, err := s.UpdateCheck(ctx, orgSlug, check.UID, &UpdateCheckRequest{Regions: &pinned})
	if err != nil {
		return skip(SetRegionsSkipRejected, err)
	}

	return nil, updated.Warnings
}

// dryRunPin runs the validations a single-check PATCH would, writing nothing.
func (s *Service) dryRunPin(ctx context.Context, check *models.Check, pinned []string) error {
	req := &UpdateCheckRequest{Regions: &pinned}

	if _, err := s.resolveUpdatePlacement(
		ctx, check, s.updatePlacementSubject(check, req), updatePlacementRequest(req), false,
	); err != nil {
		return err
	}

	return s.validateRegionGates(check.Type, pinned)
}

// mergeWarnings appends the warnings of dst not already present.
func mergeWarnings(dst, extra []base.ValidationErrorField) []base.ValidationErrorField {
	for idx := range extra {
		if !slices.Contains(dst, extra[idx]) {
			dst = append(dst, extra[idx])
		}
	}

	return dst
}
