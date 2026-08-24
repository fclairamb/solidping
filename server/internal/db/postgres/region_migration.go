package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ListCheckJobsByRegion returns every check_job carrying the given region
// slug, across all organizations. See db.Service for the full contract.
func (s *Service) ListCheckJobsByRegion(ctx context.Context, region string) ([]*models.CheckJob, error) {
	var jobs []*models.CheckJob

	if err := s.db.NewSelect().
		Model(&jobs).
		Where("region = ?", region).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list check jobs by region: %w", err)
	}

	return jobs, nil
}

// ListChecksReferencingRegion returns every non-deleted check that names the
// region in `checks.regions` OR owns a check_jobs row carrying it, across all
// organizations. See db.Service for the full contract.
func (s *Service) ListChecksReferencingRegion(ctx context.Context, region string) ([]*models.Check, error) {
	var uids []string

	if err := s.db.NewSelect().
		ColumnExpr("DISTINCT c.uid").
		TableExpr("checks AS c").
		Where("c.deleted_at IS NULL").
		Where(
			"(? = ANY(c.regions) OR EXISTS ("+
				"SELECT 1 FROM check_jobs cj WHERE cj.check_uid = c.uid AND cj.region = ?))",
			region, region,
		).
		Scan(ctx, &uids); err != nil {
		return nil, fmt.Errorf("list checks referencing region: %w", err)
	}

	return s.loadChecksByUIDs(ctx, uids)
}

// ListChecksWithStaleJobRegions returns enabled, non-deleted checks whose
// check_jobs no longer line up with `checks.regions`. See db.Service for the
// full contract.
func (s *Service) ListChecksWithStaleJobRegions(ctx context.Context) ([]*models.Check, error) {
	var uids []string

	// (a) a job sitting in a region the check no longer declares. Guarded on a
	// non-empty regions array so a legitimately region-less check — which owns
	// exactly one job with a NULL region — is never dragged in.
	if err := s.db.NewSelect().
		ColumnExpr("DISTINCT cj.check_uid").
		TableExpr("check_jobs AS cj").
		Join("JOIN checks AS c ON c.uid = cj.check_uid").
		Where("c.deleted_at IS NULL").
		Where("c.enabled = ?", true).
		Where("coalesce(array_length(c.regions, 1), 0) > 0").
		Where("(cj.region IS NULL OR NOT (cj.region = ANY(c.regions)))").
		Scan(ctx, &uids); err != nil {
		return nil, fmt.Errorf("list checks with stale job regions: %w", err)
	}

	// (b) the symmetric hole: a region the check declares that has no job at
	// all. Same drift, opposite side — after a rename both show up together.
	var missing []string

	if err := s.db.NewSelect().
		ColumnExpr("DISTINCT c.uid").
		TableExpr("checks AS c").
		Where("c.deleted_at IS NULL").
		Where("c.enabled = ?", true).
		Where("EXISTS (SELECT 1 FROM unnest(c.regions) AS r WHERE NOT EXISTS ("+
			"SELECT 1 FROM check_jobs cj WHERE cj.check_uid = c.uid AND cj.region = r))").
		Scan(ctx, &missing); err != nil {
		return nil, fmt.Errorf("list checks missing region jobs: %w", err)
	}

	return s.loadChecksByUIDs(ctx, mergeUIDs(uids, missing))
}

// MigrateCheckRegionSlug rewrites `checks.regions` in ONE transaction,
// replacing `from` with `to` everywhere it appears. See db.Service for the
// full contract.
func (s *Service) MigrateCheckRegionSlug(
	ctx context.Context, from, target string,
) ([]*models.Check, error) {
	var updated []*models.Check

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var uids []string

		if err := tx.NewSelect().
			ColumnExpr("c.uid").
			TableExpr("checks AS c").
			Where("c.deleted_at IS NULL").
			Where("? = ANY(c.regions)", from).
			Order("c.uid").
			For("UPDATE").
			Scan(ctx, &uids); err != nil {
			return fmt.Errorf("select checks to migrate: %w", err)
		}

		if len(uids) == 0 {
			return nil
		}

		var rows []*models.Check
		if err := tx.NewSelect().
			Model(&rows).
			Where("uid IN (?)", bun.List(uids)).
			Scan(ctx); err != nil {
			return fmt.Errorf("load checks to migrate: %w", err)
		}

		now := time.Now()

		for _, check := range rows {
			next, changed := models.MigrateRegionList(check.Regions, from, target)
			if !changed {
				continue
			}

			if _, err := tx.NewUpdate().
				Model((*models.Check)(nil)).
				Set("regions = ?", pgdialect.Array(next)).
				Set("updated_at = ?", now).
				Where("uid = ?", check.UID).
				Exec(ctx); err != nil {
				return fmt.Errorf("update check regions: %w", err)
			}

			check.Regions = next
			updated = append(updated, check)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return updated, nil
}

// loadChecksByUIDs hydrates the check rows for a UID set, dropping soft-deleted
// rows. Returns nil for an empty set rather than issuing a pointless query.
func (s *Service) loadChecksByUIDs(ctx context.Context, uids []string) ([]*models.Check, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	var checks []*models.Check
	if err := s.db.NewSelect().
		Model(&checks).
		Where("uid IN (?)", bun.List(uids)).
		Where("deleted_at IS NULL").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("load checks by uid: %w", err)
	}

	return checks, nil
}

// mergeUIDs unions two UID slices, preserving first-seen order.
func mergeUIDs(first, second []string) []string {
	seen := make(map[string]bool, len(first)+len(second))
	out := make([]string, 0, len(first)+len(second))

	for _, list := range [][]string{first, second} {
		for _, uid := range list {
			if seen[uid] {
				continue
			}

			seen[uid] = true

			out = append(out, uid)
		}
	}

	return out
}
