package checks

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// ReplacementRequest asks ReplaceAutoChecks to move automatically placed
// checks off one region (spec 2026-09-25-06 A2).
type ReplacementRequest struct {
	// Region is the region the checks are moved off.
	Region string
	// CheckUIDs are the candidate checks: the ones holding a job in Region.
	// Pinned, disabled and deleted checks among them are left alone.
	CheckUIDs []string
	// Healthy is the set of cloud regions a check may be moved to. The region
	// sweep passes its own view of the same instant it decided Region is dark,
	// so the two can never disagree.
	Healthy map[string]bool
	// Reason is recorded on the check.placement_changed event.
	Reason string
}

// PlacementChange is one check moved from one region to another.
type PlacementChange struct {
	OrganizationUID string
	CheckUID        string
	From            string
	To              string
}

// ReplaceAutoChecks moves every AUTOMATICALLY placed check among
// req.CheckUIDs off req.Region: the region is replaced, in place, by the next
// healthy region in the check's own candidate order that it does not already
// use. For each move it
//
//   - rewrites checks.regions — the auto placement lives there, which is what
//     keeps the boot repair from undoing it;
//   - reconciles the check's jobs through reconcileCheckJobs, the same
//     mechanics MigrateRegion uses (the old region's job goes, the new one is
//     materialized with the right phase and plan weight);
//   - makes the new job due NOW: the check has already been blind for about
//     five minutes by the time a region reads dark;
//   - records a check.placement_changed event {from, to, reason}.
//
// A run of the old job still in flight may land later: its result keeps its
// real region, and its lease release fails harmlessly because the job row is
// gone (checkjobsvc's release is keyed on the job UID).
//
// A check with no healthy candidate left keeps its placement and goes stale,
// exactly like a pinned one. Nothing ever moves a check back: a recovered
// region is simply a candidate again.
func (s *Service) ReplaceAutoChecks(ctx context.Context, req ReplacementRequest) ([]PlacementChange, error) {
	if req.Region == "" || len(req.CheckUIDs) == 0 || regions.IsPrivateRegion(req.Region) {
		return nil, nil
	}

	var candidates []*models.Check

	if err := s.db.DB().NewSelect().
		Model(&candidates).
		Where("uid IN (?)", bun.List(req.CheckUIDs)).
		Where("deleted_at IS NULL").
		Where("placement = ?", models.PlacementAuto).
		Order("uid").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list auto-placed checks in region %s: %w", req.Region, err)
	}

	envs := make(map[string]*placementEnv)
	changes := make([]PlacementChange, 0, len(candidates))

	for _, check := range candidates {
		if !check.Enabled || check.IsPassive() || !slices.Contains(check.Regions, req.Region) {
			continue
		}

		env, ok := envs[check.OrganizationUID]
		if !ok {
			loaded, err := s.loadPlacementEnv(ctx, check.OrganizationUID)
			if err != nil {
				return changes, err
			}

			env = loaded
			envs[check.OrganizationUID] = env
		}

		change, moved, err := s.replaceOne(ctx, check, env, &req)
		if err != nil {
			return changes, err
		}

		if moved {
			changes = append(changes, change)
		}
	}

	return changes, nil
}

// replaceOne moves one check off the region, when a healthy candidate exists.
func (s *Service) replaceOne(
	ctx context.Context, check *models.Check, env *placementEnv, req *ReplacementRequest,
) (PlacementChange, bool, error) {
	eligible := regions.Eligible(&regions.PlacementInput{
		Candidates:   env.candidates,
		Pool:         check.RegionPool,
		Required:     requiredCapabilities(check.Type, check.Config),
		Capabilities: env.capabilities,
	})

	next, target, ok := regions.Replace(check.Regions, eligible, req.Healthy, req.Region)
	if !ok {
		slog.InfoContext(ctx, "Auto-placed check stays in a dark region: no healthy candidate left",
			"checkUid", check.UID, "region", req.Region)

		return PlacementChange{}, false, nil
	}

	if err := s.db.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Regions: &next}); err != nil {
		return PlacementChange{}, false, fmt.Errorf("re-place check %s: %w", check.UID, err)
	}

	check.Regions = next

	if err := s.reconcileCheckJobs(ctx, check, false); err != nil {
		return PlacementChange{}, false, fmt.Errorf("reconcile re-placed check %s: %w", check.UID, err)
	}

	now := s.now()

	if _, err := s.db.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", now).
		Set("effective_scheduled_at = ?", now).
		Set("updated_at = ?", now).
		Where("check_uid = ?", check.UID).
		Where("region = ?", target).
		Exec(ctx); err != nil {
		return PlacementChange{}, false, fmt.Errorf("make re-placed job due for check %s: %w", check.UID, err)
	}

	s.recordPlacementChanged(ctx, check, req.Region, target, req.Reason)

	slog.InfoContext(ctx, "Auto-placed check moved off a dark region",
		"checkUid", check.UID, "from", req.Region, "to", target, "reason", req.Reason)

	return PlacementChange{
		OrganizationUID: check.OrganizationUID,
		CheckUID:        check.UID,
		From:            req.Region,
		To:              target,
	}, true, nil
}

// recordPlacementChanged writes the check.placement_changed event. Best
// effort: the move itself already happened, and a lost audit row must not
// undo it.
func (s *Service) recordPlacementChanged(ctx context.Context, check *models.Check, from, target, reason string) {
	event := models.NewEvent(check.OrganizationUID, models.EventTypeCheckPlacementChanged, models.ActorTypeSystem)
	event.CheckUID = &check.UID
	event.Payload = models.JSONMap{
		models.PlacementEventPayloadFrom:   from,
		models.PlacementEventPayloadTo:     target,
		models.PlacementEventPayloadReason: reason,
		eventPayloadCheckUIDKey:            check.UID,
		eventPayloadCheckSlugKey:           check.Slug,
		eventPayloadCheckNameKey:           check.Name,
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.ErrorContext(ctx, "Failed to record a check.placement_changed event",
			"checkUid", check.UID, "error", err)
	}
}
