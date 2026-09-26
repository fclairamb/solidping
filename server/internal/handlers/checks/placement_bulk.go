package checks

import (
	"context"
	"fmt"
	"slices"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Reasons a check is left out of the bulk switch to automatic placement.
const (
	AutoPlacementSkipNotFound = "not_found"
	AutoPlacementSkipAlready  = "already_auto"
	AutoPlacementSkipPassive  = "passive"
	AutoPlacementSkipNoRegion = "no_region"
	AutoPlacementSkipPrivate  = "private_region"
	AutoPlacementSkipReadOnly = "read_only"
)

// AutoPlacementRequest is the body of POST /api/v1/orgs/:org/checks/auto-placement.
type AutoPlacementRequest struct {
	// CheckUIDs limits the switch to these checks. Empty: every pinned check
	// of the org that can be placed automatically.
	CheckUIDs []string `json:"checkUids,omitempty"`
	// DryRun reports what would be switched without writing anything.
	DryRun bool `json:"dryRun,omitempty"`
}

// AutoPlacementItem is one check switched (or that would be).
type AutoPlacementItem struct {
	UID         string   `json:"uid"`
	Slug        *string  `json:"slug,omitempty"`
	Name        *string  `json:"name,omitempty"`
	Regions     []string `json:"regions"`
	RegionCount int      `json:"regionCount"`
}

// AutoPlacementSkip is one requested check left pinned, and why.
type AutoPlacementSkip struct {
	UID    string  `json:"uid"`
	Slug   *string `json:"slug,omitempty"`
	Reason string  `json:"reason"`
}

// AutoPlacementResponse is the answer of the bulk switch.
type AutoPlacementResponse struct {
	Data    []AutoPlacementItem `json:"data"`
	Skipped []AutoPlacementSkip `json:"skipped"`
	DryRun  bool                `json:"dryRun"`
}

// SwitchToAutoPlacement is the checks list's bulk action "Switch to automatic
// placement" (spec 2026-09-25-06 A3). It performs the exact conversion the
// migration applies to checks on the system default regions: placement auto,
// regionCount = the current region count, empty pool, regions untouched. So
// the switch changes neither the cost nor where the check runs today; it only
// lets the scheduler move the check when one of its regions goes dark.
//
// Private regions are pinned-only, so a check naming one is skipped, as are
// passive checks (no region at all). With no checkUids, every eligible pinned,
// non-internal check of the org is switched; skips are only reported for
// checks the caller named.
func (s *Service) SwitchToAutoPlacement(
	ctx context.Context, orgSlug string, req *AutoPlacementRequest,
) (*AutoPlacementResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	var rows []*models.Check

	query := s.db.DB().NewSelect().
		Model(&rows).
		Where("organization_uid = ?", org.UID).
		Where("deleted_at IS NULL").
		Order("created_at", "uid")

	named := len(req.CheckUIDs) > 0
	if named {
		query = query.Where("uid IN (?)", bun.List(req.CheckUIDs))
	} else {
		query = query.Where("placement = ?", models.PlacementPinned).Where("internal = ?", false)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("list checks to switch to automatic placement: %w", err)
	}

	resp := &AutoPlacementResponse{Data: []AutoPlacementItem{}, Skipped: []AutoPlacementSkip{}, DryRun: req.DryRun}
	found := make(map[string]bool, len(rows))

	for _, check := range rows {
		found[check.UID] = true

		if reason := autoPlacementSkipReason(ctx, check); reason != "" {
			if named {
				resp.Skipped = append(resp.Skipped, AutoPlacementSkip{UID: check.UID, Slug: check.Slug, Reason: reason})
			}

			continue
		}

		if !req.DryRun {
			if err := s.switchOneToAuto(ctx, check); err != nil {
				return nil, err
			}
		}

		resp.Data = append(resp.Data, AutoPlacementItem{
			UID: check.UID, Slug: check.Slug, Name: check.Name,
			Regions: check.Regions, RegionCount: len(check.Regions),
		})
	}

	for _, uid := range req.CheckUIDs {
		if !found[uid] {
			resp.Skipped = append(resp.Skipped, AutoPlacementSkip{UID: uid, Reason: AutoPlacementSkipNotFound})
		}
	}

	return resp, nil
}

// autoPlacementSkipReason tells why a check cannot be switched, empty when it
// can.
func autoPlacementSkipReason(ctx context.Context, check *models.Check) string {
	switch {
	case check.IsAutoPlaced():
		return AutoPlacementSkipAlready
	case check.IsPassive():
		return AutoPlacementSkipPassive
	case len(check.Regions) == 0:
		return AutoPlacementSkipNoRegion
	case slices.ContainsFunc(check.Regions, regions.IsPrivateRegion):
		return AutoPlacementSkipPrivate
	case assertDemoMayWriteCheck(ctx, check) != nil:
		return AutoPlacementSkipReadOnly
	default:
		return ""
	}
}

// switchOneToAuto writes the conversion. The regions (and so the jobs) are
// untouched: nothing to reconcile.
func (s *Service) switchOneToAuto(ctx context.Context, check *models.Check) error {
	placement := models.PlacementAuto
	count := len(check.Regions)

	if err := s.db.UpdateCheck(ctx, check.UID, &models.CheckUpdate{
		Placement:       &placement,
		RegionCount:     &count,
		ClearRegionPool: true,
	}); err != nil {
		return fmt.Errorf("switch check %s to automatic placement: %w", check.UID, err)
	}

	return nil
}
